package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/provider"
)

// buildFixture is a cloud holding three earlier my-dev builds in blr1
// and, once the fake Packer has run, the new one with ID 100.
type buildFixture struct {
	images   *fakeImages
	newImage provider.Image
	packErr  error
	editOut  config.EditOutcome
	editErr  error
	edited   []string // "path ref" per SetImage call
}

func newBuildFixture() *buildFixture {
	f := &buildFixture{images: &fakeImages{}, editOut: config.EditOutcome{Written: true}}
	for i := 1; i <= 3; i++ {
		f.images.images = append(f.images.images, snap(fmt.Sprint(i), t0.Add(time.Duration(i)*time.Hour)))
	}
	f.newImage = snap("100", t0.Add(24*time.Hour))
	return f
}

func (f *buildFixture) deps(t *testing.T) BuildDeps {
	return BuildDeps{
		Images: f.images,
		Packer: func(_ context.Context, dir, token string, _ io.Writer) (Artifact, error) {
			f.images.calls = append(f.images.calls, "Packer")
			if _, err := os.Stat(filepath.Join(dir, bundleHCL)); err != nil {
				t.Errorf("Packer ran before the bundle was generated: %v", err)
			}
			if token != "do-token" {
				t.Errorf("Packer token = %q, want do-token", token)
			}
			if f.packErr != nil {
				return Artifact{}, f.packErr
			}
			f.images.images = append(f.images.images, f.newImage)
			return Artifact{ID: f.newImage.ID, Region: "blr1"}, nil
		},
		SetImage: func(path, ref string) (config.EditOutcome, error) {
			f.images.calls = append(f.images.calls, "SetImage")
			f.edited = append(f.edited, path+" "+ref)
			return f.editOut, f.editErr
		},
	}
}

func buildOpts(t *testing.T) BuildOptions {
	return BuildOptions{Bundle: bundleOpts(t, selected(nil)), Token: "do-token", KeepPrevious: 1, Prune: true}
}

func TestBuild_HappyPathOrder(t *testing.T) {
	f := newBuildFixture()
	o := buildOpts(t)

	res, err := Build(context.Background(), f.deps(t), o)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		"Packer", "GetImage 100", "SetImage", "ListImages my-dev",
		"GetImage 1", "DeleteImage 1", "GetImage 2", "DeleteImage 2",
	}
	if !slices.Equal(f.images.calls, want) {
		t.Errorf("calls = %v\nwant    %v", f.images.calls, want)
	}
	if res.ImageID != "100" || res.Reference != "tag:my-dev" {
		t.Errorf("result = %+v, want image 100 as tag:my-dev", res)
	}
	if res.ConfigPath != o.Bundle.Selected.Path || res.BundleDir != o.Bundle.OutputDir {
		t.Errorf("result paths = %q, %q", res.ConfigPath, res.BundleDir)
	}
	if !slices.Equal(f.edited, []string{o.Bundle.Selected.Path + " tag:my-dev"}) {
		t.Errorf("SetImage calls = %v", f.edited)
	}
	if res.Prune == nil || !slices.Equal(res.Prune.Deleted, []string{"1", "2"}) {
		t.Errorf("prune = %+v, want 1 and 2 deleted", res.Prune)
	}
}

func TestBuild_NoPruneListsNothing(t *testing.T) {
	f := newBuildFixture()
	o := buildOpts(t)
	o.Prune = false

	res, err := Build(context.Background(), f.deps(t), o)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := f.images.count("ListImages") + f.images.count("DeleteImage"); n != 0 {
		t.Errorf("calls = %v, want no list or delete", f.images.calls)
	}
	if res.Prune != nil {
		t.Errorf("Prune = %+v, want nil", res.Prune)
	}
}

func TestBuild_PackerFailureNeverPrunes(t *testing.T) {
	f := newBuildFixture()
	f.packErr = &OpError{Phase: "packer-build", ImageCreated: true, ImageID: "100", Err: errors.New("tagging failed")}

	_, err := Build(context.Background(), f.deps(t), buildOpts(t))
	var op *OpError
	if !errors.As(err, &op) || op.Phase != "packer-build" || op.ImageID != "100" {
		t.Fatalf("err = %v, want the packer-build OpError naming image 100", err)
	}
	if n := f.images.count("ListImages") + f.images.count("DeleteImage") + f.images.count("SetImage"); n != 0 {
		t.Errorf("calls = %v, want no writeback or prune after a failed build", f.images.calls)
	}
}

func TestBuild_UnverifiedArtifactNeverPrunes(t *testing.T) {
	for name, mut := range map[string]func(*provider.Image){
		"missing marker": func(img *provider.Image) { img.Tags = []string{MarkerManaged, "my-dev"} },
		"not available":  func(img *provider.Image) { img.Status = "pending" },
		"other shell": func(img *provider.Image) {
			img.Tags = SnapshotTags("my-dev", "x86_64", "blr1", "zsh")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBuildFixture()
			mut(&f.newImage)

			_, err := Build(context.Background(), f.deps(t), buildOpts(t))
			var op *OpError
			if !errors.As(err, &op) || op.Phase != "verify" || !op.ImageCreated || op.ImageID != "100" {
				t.Fatalf("err = %#v, want verify OpError with image 100 created", err)
			}
			if n := f.images.count("ListImages") + f.images.count("DeleteImage") + f.images.count("SetImage"); n != 0 {
				t.Errorf("calls = %v, want no writeback or prune", f.images.calls)
			}
		})
	}
}

func TestBuild_WritebackSkipGivesSnippet(t *testing.T) {
	f := newBuildFixture()
	f.editOut = config.EditOutcome{Reason: config.SkipSymlink}

	res, err := Build(context.Background(), f.deps(t), buildOpts(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Snippet != `image = "tag:my-dev"` {
		t.Errorf("Snippet = %q", res.Snippet)
	}
	if res.Writeback.Reason != config.SkipSymlink {
		t.Errorf("Writeback = %+v", res.Writeback)
	}
}

func TestBuild_WritebackErrorStillPrunes(t *testing.T) {
	f := newBuildFixture()
	f.editErr = errors.New("disk full")

	res, err := Build(context.Background(), f.deps(t), buildOpts(t))
	var op *OpError
	if !errors.As(err, &op) || op.Phase != "writeback" || !op.ImageCreated || op.ImageID != "100" {
		t.Fatalf("err = %v, want writeback OpError naming image 100", err)
	}
	if f.images.count("DeleteImage") != 2 {
		t.Errorf("calls = %v, want retention to run anyway", f.images.calls)
	}
	if res.ImageID != "100" || res.Snippet != `image = "tag:my-dev"` {
		t.Errorf("result = %+v, want image and snippet still reported", res)
	}
}

func TestBuild_WritebackAndPruneFailuresBothReported(t *testing.T) {
	f := newBuildFixture()
	f.editErr = errors.New("disk full")
	f.images.deleteErr = map[string]error{"2": errors.New("403 forbidden")}

	_, err := Build(context.Background(), f.deps(t), buildOpts(t))
	var op *OpError
	if !errors.As(err, &op) || op.Phase != "writeback" {
		t.Fatalf("err = %v, want the writeback OpError", err)
	}
	for _, want := range []string{"disk full", "403 forbidden", "deleted 1", "pending 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestBuild_PruneFailureReportsImage(t *testing.T) {
	f := newBuildFixture()
	f.images.deleteErr = map[string]error{"2": errors.New("403 forbidden")}

	res, err := Build(context.Background(), f.deps(t), buildOpts(t))
	var op *OpError
	if !errors.As(err, &op) || op.Phase != "prune" || !op.ImageCreated || op.ImageID != "100" {
		t.Fatalf("err = %v, want prune OpError naming image 100", err)
	}
	if !strings.Contains(op.Cleanup, "deleted 1") || !strings.Contains(op.Cleanup, "pending 1") {
		t.Errorf("Cleanup = %q, want deleted 1, pending 1", op.Cleanup)
	}
	if slices.Contains(f.images.calls, "DeleteImage 100") {
		t.Errorf("calls = %v: the new image must never be deleted", f.images.calls)
	}
	if res.Prune == nil || !slices.Equal(res.Prune.Pending, []string{"2"}) {
		t.Errorf("Prune = %+v, want 2 pending", res.Prune)
	}
}

func TestBuild_NegativeKeepFailsBeforeAnything(t *testing.T) {
	f := newBuildFixture()
	o := buildOpts(t)
	o.KeepPrevious = -1

	if _, err := Build(context.Background(), f.deps(t), o); err == nil {
		t.Fatal("Build succeeded with keep-previous -1")
	}
	if len(f.images.calls) != 0 {
		t.Errorf("calls = %v, want none", f.images.calls)
	}
	if _, err := os.Stat(o.Bundle.OutputDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bundle dir created: %v", err)
	}
}

func TestManagedBundleDir_UniqueUnderCache(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	a, err := ManagedBundleDir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := ManagedBundleDir()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two calls both returned %s", a)
	}
	for _, d := range []string{a, b} {
		if filepath.Dir(d) != filepath.Join(cache, "bivouac", "images") {
			t.Errorf("%s is not under %s/bivouac/images", d, cache)
		}
	}
}
