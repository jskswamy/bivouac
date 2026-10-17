package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/spf13/cobra"
)

// imageFake is an image-managing provider for list, tag and prune. It
// records every mutating call so a test can assert none happened.
type imageFake struct {
	confirmedUpProvider
	images    []provider.Image
	deleteErr map[string]error
	deleted   []string
	tagged    [][2]string
	listTags  []string
}

func (p *imageFake) ListImages(_ context.Context, tag string) ([]provider.Image, error) {
	p.listTags = append(p.listTags, tag)
	var out []provider.Image
	for _, img := range p.images {
		if tag == "" || slices.Contains(img.Tags, tag) {
			out = append(out, img)
		}
	}
	return out, nil
}

func (p *imageFake) GetImage(_ context.Context, id string) (provider.Image, error) {
	for _, img := range p.images {
		if img.ID == id && !slices.Contains(p.deleted, id) {
			return img, nil
		}
	}
	return provider.Image{}, provider.ErrImageNotFound
}

func (p *imageFake) TagImage(_ context.Context, id, tag string) error {
	p.tagged = append(p.tagged, [2]string{id, tag})
	return nil
}

func (p *imageFake) DeleteImage(_ context.Context, id string) error {
	if err := p.deleteErr[id]; err != nil {
		return err
	}
	p.deleted = append(p.deleted, id)
	return nil
}

func (p *imageFake) SizeDiskGB(context.Context, string) (int, error) { return 25, nil }

// snapIn is a ready my-dev Bivouac snapshot built in region.
func snapIn(id, region string, hour int) provider.Image {
	return provider.Image{
		ID: id, Name: "bivouac-" + id, Status: "available", Type: "snapshot", Regions: []string{region},
		Created: time.Date(2026, 10, 1, hour, 0, 0, 0, time.UTC),
		Tags:    image.SnapshotTags("my-dev", "x86_64", region, "fish"),
	}
}

// foreignSnap shares the user tag but carries no markers.
func foreignSnap(id string) provider.Image {
	return provider.Image{
		ID: id, Name: "someone-else", Status: "available", Type: "snapshot", Regions: []string{"nyc3"},
		Created: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Tags: []string{"my-dev"},
	}
}

func newImageFake(t *testing.T, imgs ...provider.Image) *imageFake {
	return &imageFake{confirmedUpProvider: confirmedUpProvider{t: t}, images: imgs}
}

func fakeResolve(p *imageFake) func(context.Context) (provider.Provider, error) {
	return func(context.Context) (provider.Provider, error) { return p, nil }
}

// execImageSub runs `image <sub.Name()> args...` with sub swapped in for
// the registered command, so the fake provider is the one used.
func execImageSub(t *testing.T, sub *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("DIGITALOCEAN_TOKEN", "test-token")
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() != "image" {
			continue
		}
		for _, old := range c.Commands() {
			if old.Name() == sub.Name() {
				c.RemoveCommand(old)
			}
		}
		c.AddCommand(sub)
	}
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"image", sub.Name()}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func listFixture(t *testing.T) *imageFake {
	notReady := snapIn("30", "nyc3", 5)
	notReady.Status = "pending"
	return newImageFake(t, snapIn("10", "nyc3", 1), snapIn("20", "blr1", 2), notReady, foreignSnap("40"))
}

func TestImageList_Table(t *testing.T) {
	p := listFixture(t)
	stdout, _, err := execImageSub(t, newImageListCmdWithProvider(fakeResolve(p)))
	if err != nil {
		t.Fatalf("image list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if got := strings.Fields(lines[0]); !slices.Equal(got, []string{"ID", "TAGS", "CREATED", "REGIONS", "STATUS"}) {
		t.Errorf("header = %v", got)
	}
	want := map[string]string{"10": "managed", "20": "managed", "30": "not ready", "40": "foreign"}
	for id, status := range want {
		found := false
		for _, l := range lines[1:] {
			if strings.HasPrefix(l, id+" ") {
				found = true
				if !strings.HasSuffix(strings.TrimSpace(l), status) {
					t.Errorf("row %q, want status %q", l, status)
				}
			}
		}
		if !found {
			t.Errorf("no row for image %s:\n%s", id, stdout)
		}
	}
}

func TestImageList_Filters(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want []string
	}{
		"tag":    {[]string{"--tag", "my-dev"}, []string{"10", "20", "30", "40"}},
		"region": {[]string{"--region", "blr1"}, []string{"20"}},
		"arch":   {[]string{"--arch", "x86_64"}, []string{"10", "20", "30"}},
		"none":   {nil, []string{"10", "20", "30", "40"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := listFixture(t)
			p.images = append(p.images, provider.Image{ID: "50", Status: "available", Type: "snapshot", Regions: []string{"nyc3"}})
			if name == "none" {
				tc.want = append(tc.want, "50")
			}
			stdout, _, err := execImageSub(t, newImageListCmdWithProvider(fakeResolve(p)), append(tc.args, "--json")...)
			if err != nil {
				t.Fatalf("image list: %v", err)
			}
			var rows []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, stdout)
			}
			var got []string
			for _, r := range rows {
				got = append(got, r.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImageList_JSONFields(t *testing.T) {
	p := listFixture(t)
	stdout, _, err := execImageSub(t, newImageListCmdWithProvider(fakeResolve(p)), "--json")
	if err != nil {
		t.Fatalf("image list: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	for _, r := range rows {
		for _, k := range []string{"id", "name", "tags", "created", "regions", "status", "managed"} {
			if _, ok := r[k]; !ok {
				t.Errorf("row %v missing %q", r, k)
			}
		}
		if r["id"] == "10" && (r["managed"] != true || r["status"] != "managed") {
			t.Errorf("image 10 = %v, want managed", r)
		}
		if r["id"] == "40" && r["managed"] != false {
			t.Errorf("foreign image 40 = %v, want managed false", r)
		}
	}
}

func TestImageList_RejectsRepoAndName(t *testing.T) {
	for _, flag := range []string{"--repo", "--name"} {
		p := listFixture(t)
		if _, _, err := execImageSub(t, newImageListCmdWithProvider(fakeResolve(p)), flag, "x"); err == nil {
			t.Errorf("image list %s succeeded, want an error", flag)
		}
	}
}

func TestImageCommands_MetadataAndManifest(t *testing.T) {
	root := newRootCmd()
	got := manifestFromCommand(t, []string{"manifest"})
	for _, name := range []string{"list", "tag", "prune"} {
		c, _, err := root.Find([]string{"image", name})
		if err != nil || c.Name() != name {
			t.Fatalf("image %s not registered: %v", name, err)
		}
		meta := metadataFor(c)
		if !meta.SupportsJSON || !meta.Network || meta.RequiresRepo {
			t.Errorf("image %s metadata = %+v, want SupportsJSON, Network, no RequiresRepo", name, meta)
		}
		if strings.TrimSpace(c.Example) == "" {
			t.Errorf("image %s has no usage examples", name)
		}
		tool := findTool(t, got, "bivouac_image_"+name)
		if !tool.Metadata.JsonOutput {
			t.Errorf("image %s must advertise jsonOutput", name)
		}
	}
	prune, _, _ := root.Find([]string{"image", "prune"})
	if meta := metadataFor(prune); !meta.Destructive || !meta.MayPrompt {
		t.Errorf("image prune metadata = %+v, want Destructive and MayPrompt", meta)
	}
	for _, flag := range []string{"tag", "keep-previous", "dry-run", "yes", "region", "arch", "json"} {
		if prune.Flags().Lookup(flag) == nil {
			t.Errorf("image prune missing --%s", flag)
		}
	}
}
