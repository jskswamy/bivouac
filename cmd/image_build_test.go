package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/xdg"
)

// buildProvider is an image-managing provider whose account holds two
// earlier my-dev builds in nyc3; the fake Packer adds image 100.
type buildProvider struct {
	confirmedUpProvider
	images    []provider.Image
	deleteErr map[string]error
	deleted   []string
	token     string
}

func buildSnap(id string, hours int) provider.Image {
	return provider.Image{
		ID: id, Status: "available", Type: "snapshot", Regions: []string{"nyc3"},
		Created: time.Date(2026, 10, 1, hours, 0, 0, 0, time.UTC),
		Tags:    image.SnapshotTags("my-dev", "x86_64", "nyc3", "fish"),
	}
}

func (p *buildProvider) ListImages(context.Context, string) ([]provider.Image, error) {
	return p.images, nil
}
func (p *buildProvider) GetImage(_ context.Context, id string) (provider.Image, error) {
	for _, img := range p.images {
		if img.ID == id {
			return img, nil
		}
	}
	return provider.Image{}, provider.ErrImageNotFound
}
func (p *buildProvider) TagImage(context.Context, string, string) error { return nil }
func (p *buildProvider) DeleteImage(_ context.Context, id string) error {
	if err := p.deleteErr[id]; err != nil {
		return err
	}
	p.deleted = append(p.deleted, id)
	return nil
}
func (p *buildProvider) SizeDiskGB(context.Context, string) (int, error) { return 25, nil }

func isolateImageBuildCache(t *testing.T) {
	t.Helper()
	realCache, _ := xdg.Path(xdg.Cache, "pkl")
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if realCache != "" {
		if fi, err := os.Stat(realCache); err == nil && fi.IsDir() {
			target := filepath.Join(cache, "bivouac", "pkl")
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Symlink(realCache, target)
		}
	}
}

// runImageBuild runs `image build` against p with a fake Packer that
// writes a marker to its progress stream and reports image 100.
func execImageBuild(t *testing.T, p *buildProvider, args ...string) (stdout, stderr string, packerRuns int, err error) {
	t.Helper()
	t.Setenv("DIGITALOCEAN_TOKEN", "test-token")
	isolateImageBuildCache(t)
	resolve := func(_ context.Context, token string) (provider.Provider, error) {
		p.token = token
		return p, nil
	}
	packer := func(_ context.Context, _, token string, progress io.Writer) (image.Artifact, error) {
		packerRuns++
		if token != "test-token" {
			t.Errorf("Packer token = %q", token)
		}
		_, _ = fmt.Fprintln(progress, "PACKER-RAN")
		p.images = append(p.images, buildSnap("100", 20))
		return image.Artifact{ID: "100", Region: "nyc3"}, nil
	}
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "image" {
			for _, sub := range c.Commands() {
				if sub.Name() == "build" {
					c.RemoveCommand(sub)
				}
			}
			c.AddCommand(newImageBuildCmdWithProvider(resolve, packer))
		}
	}
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"image", "build"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), packerRuns, err
}

func newBuildProvider(t *testing.T) *buildProvider {
	return &buildProvider{
		confirmedUpProvider: confirmedUpProvider{t: t},
		images:              []provider.Image{buildSnap("1", 1), buildSnap("2", 2)},
	}
}

func TestImageBuild_Metadata(t *testing.T) {
	root := newRootCmd()
	c, _, err := root.Find([]string{"image", "build"})
	if err != nil || c.Name() != "build" {
		t.Fatalf("image build not registered: %v", err)
	}
	meta := metadataFor(c)
	if !meta.SupportsJSON || !meta.Network || !meta.Destructive {
		t.Errorf("metadata = %+v, want SupportsJSON, Network and Destructive (it prunes)", meta)
	}
	if strings.TrimSpace(c.Example) == "" {
		t.Errorf("image build has no usage examples")
	}
	for _, flag := range []string{"config", "tag", "output", "keep-previous", "no-prune", "json"} {
		if c.Flags().Lookup(flag) == nil {
			t.Errorf("missing --%s", flag)
		}
	}
}

func TestImageBuild_InManifest(t *testing.T) {
	got := manifestFromCommand(t, []string{"manifest"})
	tool := findTool(t, got, "bivouac_image_build")
	if !tool.Metadata.JsonOutput {
		t.Errorf("image build must advertise jsonOutput")
	}
}

func TestImageBuild_ContradictoryFlags(t *testing.T) {
	imageGenerateFixture(t)
	for name, args := range map[string][]string{
		"no-prune with keep-previous": {"--tag", "my-dev", "--no-prune", "--keep-previous", "2"},
		"negative keep-previous":      {"--tag", "my-dev", "--keep-previous", "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newBuildProvider(t)
			_, _, runs, err := execImageBuild(t, p, args...)
			if err == nil {
				t.Fatal("image build succeeded, want a flag error")
			}
			if runs != 0 {
				t.Errorf("Packer ran %d times, want 0", runs)
			}
		})
	}
}

func TestImageBuild_PolicyBeforePackerAndResult(t *testing.T) {
	basePath, _ := imageGenerateFixture(t)
	p := newBuildProvider(t)

	stdout, stderr, _, err := execImageBuild(t, p, "--tag", "my-dev")
	if err != nil {
		t.Fatalf("image build: %v\n%s", err, stderr)
	}
	policy := strings.Index(stderr, "Retention:")
	if policy < 0 || policy > strings.Index(stderr, "PACKER-RAN") {
		t.Errorf("stderr does not state retention before Packer runs:\n%s", stderr)
	}
	for _, want := range []string{"100", "tag:my-dev", basePath} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !slices.Equal(p.deleted, []string{"1"}) {
		t.Errorf("deleted = %v, want only 1 (keep current + 1 previous)", p.deleted)
	}
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `image = "tag:my-dev"`) {
		t.Errorf("base.pkl not pointed at the tag:\n%s", body)
	}
}

func TestImageBuild_JSONOnSuccess(t *testing.T) {
	basePath, _ := imageGenerateFixture(t)
	p := newBuildProvider(t)

	stdout, _, _, err := execImageBuild(t, p, "--tag", "my-dev", "--json", "--no-prune")
	if err != nil {
		t.Fatalf("image build: %v", err)
	}
	var got struct {
		ImageID    string `json:"image_id"`
		Reference  string `json:"reference"`
		ConfigPath string `json:"config_path"`
		BundleDir  string `json:"bundle_dir"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if got.ImageID != "100" || got.Reference != "tag:my-dev" || got.ConfigPath != basePath || got.BundleDir == "" {
		t.Errorf("result = %+v", got)
	}
	if len(p.deleted) != 0 {
		t.Errorf("--no-prune deleted %v", p.deleted)
	}
}

func TestImageBuild_PruneFailureJSON(t *testing.T) {
	imageGenerateFixture(t)
	p := newBuildProvider(t)
	p.deleteErr = map[string]error{"1": errors.New("403 forbidden")}

	stdout, _, _, err := execImageBuild(t, p, "--tag", "my-dev", "--json")
	if err == nil {
		t.Fatal("image build succeeded despite the prune failure")
	}
	var got struct {
		Phase        string `json:"phase"`
		ImageCreated bool   `json:"image_created"`
		ImageID      string `json:"image_id"`
		Cleanup      string `json:"cleanup"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if got.Phase != "prune" || !got.ImageCreated || got.ImageID != "100" || got.Cleanup == "" || got.Error == "" {
		t.Errorf("error JSON = %+v", got)
	}
}
