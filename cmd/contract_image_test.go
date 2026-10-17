package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean/dofake"
)

var snapshotTagsLine = regexp.MustCompile(`(?m)^\s*snapshot_tags\s*=\s*(\[.*\])\s*$`)

// bundleSnapshotTags reads the tags the generated template asks the
// plugin to attach, with the region variable at its default, so the
// stand-in plugin tags what the bundle says and not what a test
// assumes it says.
func bundleSnapshotTags(dir, region string) ([]string, error) {
	hcl, err := os.ReadFile(filepath.Join(dir, "image.pkr.hcl"))
	if err != nil {
		return nil, err
	}
	m := snapshotTagsLine.FindSubmatch(hcl)
	if m == nil {
		return nil, errors.New("image.pkr.hcl has no snapshot_tags")
	}
	var tags []string
	if err := json.Unmarshal(m[1], &tags); err != nil {
		return nil, fmt.Errorf("snapshot_tags %s: %w", m[1], err)
	}
	for i, tag := range tags {
		tags[i] = strings.ReplaceAll(tag, "${var.region}", region)
	}
	return tags, nil
}

// pluginPacker stands in for `packer build` with the DigitalOcean
// plugin, doing through the fake API what the plugin does through the
// real one: create a builder droplet from the bundle's source image,
// power it off, snapshot it, attach the bundle's snapshot_tags through
// the tags API and delete the builder.
//
// The snapshot itself is taken through dofake.Snapshot: the plugin finds
// the image an action made through a route nothing in bivouac calls,
// and modelling it here would test the plugin, not bivouac.
func pluginPacker(s *dofake.Server, runs *int) packerFunc {
	return func(ctx context.Context, dir, token string, progress io.Writer) (image.Artifact, error) {
		*runs++
		raw, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
		if err != nil {
			return image.Artifact{}, err
		}
		var meta image.Metadata
		if err := json.Unmarshal(raw, &meta); err != nil {
			return image.Artifact{}, err
		}
		tags, err := bundleSnapshotTags(dir, meta.Region)
		if err != nil {
			return image.Artifact{}, err
		}
		userData, err := os.ReadFile(filepath.Join(dir, "scripts", "bootstrap.sh"))
		if err != nil {
			return image.Artifact{}, err
		}

		c := godo.NewFromToken(token)
		if c.BaseURL, err = url.Parse(s.URL() + "/"); err != nil {
			return image.Artifact{}, err
		}
		d, _, err := c.Droplets.Create(ctx, &godo.DropletCreateRequest{
			Name: "packer-builder", Region: meta.Region, Size: meta.Size,
			Image: godo.DropletCreateImage{Slug: meta.SourceImage}, UserData: string(userData),
		})
		if err != nil {
			return image.Artifact{}, fmt.Errorf("creating builder: %w", err)
		}
		_, _ = fmt.Fprintf(progress, "builder droplet %d\n", d.ID)
		if _, _, err := c.DropletActions.PowerOff(ctx, d.ID); err != nil {
			return image.Artifact{}, fmt.Errorf("powering off builder: %w", err)
		}
		snap, err := s.Snapshot(d.ID, meta.Tag+"-packer")
		if err != nil {
			return image.Artifact{}, err
		}
		for _, tag := range tags {
			if _, _, err := c.Tags.Create(ctx, &godo.TagCreateRequest{Name: tag}); err != nil {
				return image.Artifact{}, fmt.Errorf("creating tag %s: %w", tag, err)
			}
			if _, err := c.Tags.TagResources(ctx, tag, &godo.TagResourcesRequest{
				Resources: []godo.Resource{{ID: strconv.Itoa(snap.ID), Type: godo.ImageResourceType}},
			}); err != nil {
				return image.Artifact{}, fmt.Errorf("tagging snapshot with %s: %w", tag, err)
			}
		}
		if _, err := c.Droplets.Delete(ctx, d.ID); err != nil {
			return image.Artifact{}, fmt.Errorf("deleting builder: %w", err)
		}
		return image.Artifact{ID: strconv.Itoa(snap.ID), Region: meta.Region}, nil
	}
}

// execContractBuild runs `image build` with the real provider on s and
// pluginPacker standing in for Packer.
func execContractBuild(t *testing.T, s *dofake.Server, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("DIGITALOCEAN_TOKEN", "dummy-token")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	runs := 0
	resolve := func(context.Context, string) (provider.Provider, error) { return s.Provider(t), nil }
	c := newImageBuildCmdWithProvider(resolve, pluginPacker(s, &runs))
	stdout, stderr, err = execImageSub(t, c, args...)
	if runs != 1 {
		t.Errorf("Packer ran %d times, want 1", runs)
	}
	return stdout, stderr, err
}

func imageIDs(imgs []godo.Image) []int {
	out := make([]int, 0, len(imgs))
	for _, img := range imgs {
		out = append(out, img.ID)
	}
	return out
}

// deletedImages lists the image IDs deleted over HTTP, in order.
func deletedImages(s *dofake.Server) []string {
	var out []string
	for _, r := range requestsUnder(s, "DELETE", "/v2/images/") {
		out = append(out, strings.TrimPrefix(r.Path, "/v2/images/"))
	}
	return out
}

func TestContractImageBuild_EndToEnd(t *testing.T) {
	basePath, _ := imageGenerateFixture(t)
	s := contractFake(t)
	oldest := seedSnap(s, "my-dev", "nyc3", 1)
	middle := seedSnap(s, "my-dev", "nyc3", 2)
	newestOld := seedSnap(s, "my-dev", "nyc3", 3)

	stdout, stderr, err := execContractBuild(t, s, "--tag", "my-dev", "--keep-previous", "1", "--json")
	if err != nil {
		t.Fatalf("image build: %v\n%s", err, stderr)
	}
	var res struct {
		ImageID string   `json:"image_id"`
		Pruned  []string `json:"pruned"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}

	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `image = "tag:my-dev"`) {
		t.Errorf("base.pkl not pointed at the tag:\n%s", body)
	}

	wantDeleted := []string{strconv.Itoa(oldest.ID), strconv.Itoa(middle.ID)}
	if got := deletedImages(s); !slices.Equal(got, wantDeleted) {
		t.Errorf("DELETE /v2/images = %v, want %v, oldest first", got, wantDeleted)
	}
	if !slices.Equal(res.Pruned, wantDeleted) {
		t.Errorf("reported pruned = %v, want %v", res.Pruned, wantDeleted)
	}
	newID, err := strconv.Atoi(res.ImageID)
	if err != nil {
		t.Fatalf("image_id %q: %v", res.ImageID, err)
	}
	if got, want := imageIDs(s.Images()), []int{newestOld.ID, newID}; !slices.Equal(got, want) {
		t.Errorf("images left = %v, want %v (the newest old one and the new one)", got, want)
	}
	if n := len(s.Droplets()); n != 0 {
		t.Errorf("%d droplets left on the account, want the builder gone", n)
	}

	// The new image is one a launch would pick: every marker landed.
	got, err := s.Provider(t).GetImage(context.Background(), res.ImageID)
	if err != nil {
		t.Fatal(err)
	}
	if !image.Eligible(got, image.Scope{Tag: "my-dev", Arch: "x86_64", Region: "nyc3"}) {
		t.Errorf("new image %+v is not eligible for tag:my-dev in nyc3", got)
	}
}

func TestContractImageBuild_PendingSnapshotIsNotCounted(t *testing.T) {
	basePath, _ := imageGenerateFixture(t)
	s := contractFake(t)
	seedSnap(s, "my-dev", "nyc3", 1)
	seedSnap(s, "my-dev", "nyc3", 2)
	s.SetSnapshotPending(100)

	_, stderr, err := execContractBuild(t, s, "--tag", "my-dev", "--keep-previous", "0")
	var op *image.OpError
	if !errors.As(err, &op) || op.Phase != "verify" || !op.ImageCreated {
		t.Fatalf("image build = %v, want a verify failure that names the created image\n%s", err, stderr)
	}
	if got := deletedImages(s); len(got) != 0 {
		t.Errorf("deleted %v after a failed verify", got)
	}
	if n := len(s.Images()); n != 3 {
		t.Errorf("%d images on the account, want the two old ones and the pending one", n)
	}
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "tag:my-dev") {
		t.Errorf("config pointed at an unverified build:\n%s", body)
	}
}

func TestContractImageList_Paginates(t *testing.T) {
	s := contractFake(t)
	for i := range 45 {
		seedSnap(s, "my-dev", "nyc3", i%24)
	}

	stdout, _, err := execImageSub(t, newImageListCmdWithProvider(fakeResolver(t, s)), "--json")
	if err != nil {
		t.Fatalf("image list: %v", err)
	}
	var rows []imageListRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON list: %v\n%s", err, stdout)
	}
	if len(rows) != 45 {
		t.Errorf("listed %d images, want 45", len(rows))
	}
	if n := len(requestsTo(s, "GET", "/v2/images")); n < 2 {
		t.Errorf("listing took %d requests, want more than one page", n)
	}
}

func TestContractImageTag_AddsTagOverHTTP(t *testing.T) {
	s := contractFake(t)
	snap := seedSnap(s, "my-dev", "nyc3", 1)

	if _, _, err := execImageSub(t, newImageTagCmdWithProvider(fakeResolver(t, s)), strconv.Itoa(snap.ID), "stable"); err != nil {
		t.Fatalf("image tag: %v", err)
	}
	imgs := s.Images()
	if len(imgs) != 1 || !slices.Contains(imgs[0].Tags, "stable") {
		t.Fatalf("images = %+v, want %d tagged stable", imgs, snap.ID)
	}
	if !slices.Contains(imgs[0].Tags, "my-dev") || !slices.Contains(imgs[0].Tags, image.MarkerManaged) {
		t.Errorf("tags = %v, want the old tags kept", imgs[0].Tags)
	}
	if len(requestsTo(s, "POST", "/v2/tags/stable/resources")) != 1 {
		t.Error("the tag was not attached through the tags API")
	}
}

func execContractPrune(t *testing.T, s *dofake.Server, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newImagePruneCmdWithProvider(fakeResolver(t, s), func(bool) bool { return false })
	return execImageSub(t, c, args...)
}

// Another actor deletes the first candidate between its re-check and its
// delete; then the token is refused on the second. The first is done,
// whoever did it, and the run reports exactly what a retry still owes.
func TestContractImagePrune_RaceAndForbidden(t *testing.T) {
	s := contractFake(t)
	a := seedSnap(s, "my-dev", "nyc3", 1)
	b := seedSnap(s, "my-dev", "nyc3", 2)
	c := seedSnap(s, "my-dev", "nyc3", 3)
	kept := seedSnap(s, "my-dev", "nyc3", 4)
	idA, idB, idC := strconv.Itoa(a.ID), strconv.Itoa(b.ID), strconv.Itoa(c.ID)
	s.Before("DELETE", "/v2/images/"+idA, func(s *dofake.Server) { s.RemoveImage(a.ID) })
	s.Fail("DELETE", "/v2/images/"+idB, 403, 1)

	stdout, _, err := execContractPrune(t, s, "--tag", "my-dev", "--keep-previous", "0", "--yes", "--json")
	if !errors.Is(err, provider.ErrForbidden) {
		t.Fatalf("image prune = %v, want provider.ErrForbidden", err)
	}
	var res struct {
		Phase   string   `json:"phase"`
		Deleted []string `json:"deleted"`
		Pending []string `json:"pending"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if res.Phase != "prune" || !slices.Equal(res.Deleted, []string{idA}) || !slices.Equal(res.Pending, []string{idB, idC}) {
		t.Errorf("result = %+v, want deleted [%s] and pending [%s %s]", res, idA, idB, idC)
	}
	if got, want := imageIDs(s.Images()), []int{b.ID, c.ID, kept.ID}; !slices.Equal(got, want) {
		t.Errorf("images left = %v, want %v", got, want)
	}
	if got := deletedImages(s); !slices.Equal(got, []string{idA, idB}) {
		t.Errorf("DELETE /v2/images = %v, want %s then %s and nothing after the refusal", got, idA, idB)
	}
}

// godo retries a 429 by itself, honouring Retry-After, so one is
// absorbed; only a rate limit that outlasts its retries reaches the
// command, and then nothing may be deleted.
func TestContractImagePrune_RateLimited(t *testing.T) {
	t.Run("one 429 is retried", func(t *testing.T) {
		s := contractFake(t)
		old := seedSnap(s, "my-dev", "nyc3", 1)
		seedSnap(s, "my-dev", "nyc3", 2)
		s.Fail("GET", "/v2/images", 429, 1)

		if _, _, err := execContractPrune(t, s, "--tag", "my-dev", "--keep-previous", "0", "--yes"); err != nil {
			t.Fatalf("image prune: %v", err)
		}
		if got := deletedImages(s); !slices.Equal(got, []string{strconv.Itoa(old.ID)}) {
			t.Errorf("deleted %v, want %d", got, old.ID)
		}
	})
	t.Run("a persistent 429 stops it", func(t *testing.T) {
		s := contractFake(t)
		seedSnap(s, "my-dev", "nyc3", 1)
		seedSnap(s, "my-dev", "nyc3", 2)
		// One more than godo's four retries.
		s.Fail("GET", "/v2/images", 429, 5)

		_, _, err := execContractPrune(t, s, "--tag", "my-dev", "--keep-previous", "0", "--yes")
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "rate limit") {
			t.Fatalf("image prune = %v, want an error naming the rate limit", err)
		}
		if got := deletedImages(s); len(got) != 0 {
			t.Errorf("deleted %v while rate limited", got)
		}
		if n := len(requestsTo(s, "GET", "/v2/images")); n != 5 {
			t.Errorf("listing attempts = %d, want 5 (one try and four retries)", n)
		}
	})
}
