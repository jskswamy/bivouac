package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jskswamy/bivouac/internal/tool"
)

// packerManifest is where the bundle's manifest post-processor writes, so
// it must match the output named in image.pkr.hcl.tmpl.
const packerManifest = "packer-manifest.json"

// OpError is an image operation that failed part-way. Build, tag and prune
// all report through it because each can fail after the cloud has already
// changed, and the user needs to know what now exists -- an "error" alone
// would hide a snapshot that is costing them money.
type OpError struct {
	Phase        string `json:"phase"` // "generate","packer-init","packer-validate","packer-build","manifest","verify","writeback","prune"
	ImageCreated bool   `json:"image_created"`
	ImageID      string `json:"image_id,omitempty"`
	Cleanup      string `json:"cleanup,omitempty"` // human summary, e.g. "deleted 1, pending 2"
	Err          error  `json:"-"`
}

func (e *OpError) Error() string {
	msg := e.Phase + " failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.ImageID != "" {
		msg += fmt.Sprintf(" (image %s was created)", e.ImageID)
	}
	if e.Cleanup != "" {
		msg += "; " + e.Cleanup
	}
	return msg
}

func (e *OpError) Unwrap() error { return e.Err }

// Artifact is the snapshot a Packer build produced.
type Artifact struct{ ID, Region string }

// cancelledBuild is what a cancelled build may leave behind. Packer
// deletes its builder when interrupted, but nothing guarantees it got
// that far before exiting, so this says "may" rather than claiming either.
const cancelledBuild = "the build was interrupted; Packer's temporary builder droplet " +
	"(named packer-*) or a partial snapshot may remain in DigitalOcean -- check and delete them by hand"

// RunPacker runs init, validate, build in dir; the token reaches only the
// child's env. Packer's own output streams to progress as it runs, because
// a build takes many minutes and silence reads as a hang.
func RunPacker(ctx context.Context, dir, token string, progress io.Writer) (Artifact, error) {
	if progress == nil {
		progress = io.Discard
	}
	bin, err := tool.Require("packer")
	if err != nil {
		return Artifact{}, &OpError{Phase: "packer-init", Err: err}
	}
	env := packerEnv(os.Environ(), token)
	run := func(sub string) error {
		return tool.RunEnv(ctx, dir, env, progress, progress, bin, sub, ".")
	}

	for _, step := range []struct{ phase, sub string }{
		{"packer-init", "init"},
		{"packer-validate", "validate"},
	} {
		if err := run(step.sub); err != nil {
			return Artifact{}, &OpError{Phase: step.phase, Err: withCause(ctx, err)}
		}
	}

	// The manifest post-processor appends to an existing file, so one left
	// by an earlier run would otherwise pass for this run's artifact.
	manifestPath := filepath.Join(dir, packerManifest)
	before, err := os.ReadFile(manifestPath) // #nosec G304 -- the bundle's own output file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, &OpError{Phase: "manifest", Err: err}
	}
	buildErr := run("build")
	art, manifestErr := readFreshManifest(manifestPath, before)

	if buildErr != nil {
		// A manifest from this run means the snapshot exists even though
		// Packer failed afterwards; its ID is the one thing the user needs.
		op := &OpError{Phase: "packer-build", Err: withCause(ctx, buildErr)}
		if manifestErr == nil {
			op.ImageCreated, op.ImageID = true, art.ID
		}
		if ctx.Err() != nil {
			op.Cleanup = cancelledBuild
		}
		return Artifact{}, op
	}
	if manifestErr != nil {
		return Artifact{}, &OpError{
			Phase:   "manifest",
			Err:     manifestErr,
			Cleanup: "Packer reported success, so a snapshot may exist in DigitalOcean without its ID being known",
		}
	}
	return art, nil
}

// readFreshManifest parses the manifest only if this run changed it.
func readFreshManifest(path string, before []byte) (Artifact, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the bundle's own output file
	if errors.Is(err, os.ErrNotExist) {
		return Artifact{}, fmt.Errorf("packer wrote no %s", packerManifest)
	}
	if err != nil {
		return Artifact{}, err
	}
	if before != nil && bytes.Equal(raw, before) {
		return Artifact{}, fmt.Errorf("packer did not update %s; it holds only earlier runs", packerManifest)
	}
	return ParseManifest(raw)
}

// withCause makes a cancelled run report the cancellation, which is what
// the caller asked about, rather than the signal that ended the child.
func withCause(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("%w (packer: %v)", ctx.Err(), err)
}

// packerEnv is environ with token as the only DigitalOcean credential.
//
// PKR_VAR_do_token goes too: Packer reads it into the template's do_token
// variable ahead of the DIGITALOCEAN_TOKEN default, so a stale one in the
// invoking shell would silently build in a different account.
func packerEnv(environ []string, token string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if strings.HasPrefix(kv, "DIGITALOCEAN_TOKEN=") || strings.HasPrefix(kv, "PKR_VAR_do_token=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "DIGITALOCEAN_TOKEN="+token)
}

// ParseManifest returns the last build's artifact: the manifest
// post-processor appends each run, so the last entry is the newest. The
// DigitalOcean builder's artifact ID is "region:snapshotID", e.g.
// "blr1:123456" -> {ID:"123456", Region:"blr1"}.
func ParseManifest(raw []byte) (Artifact, error) {
	var m struct {
		Builds []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"builds"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Artifact{}, fmt.Errorf("parse %s: %w", packerManifest, err)
	}
	if len(m.Builds) == 0 {
		return Artifact{}, fmt.Errorf("%s records no builds", packerManifest)
	}
	id := m.Builds[len(m.Builds)-1].ArtifactID
	region, snapshot, ok := strings.Cut(id, ":")
	if !ok || region == "" {
		return Artifact{}, fmt.Errorf("artifact ID %q in %s is not region:id", id, packerManifest)
	}
	if _, err := strconv.ParseUint(snapshot, 10, 64); err != nil {
		return Artifact{}, fmt.Errorf("artifact ID %q in %s has a non-numeric image ID", id, packerManifest)
	}
	return Artifact{ID: snapshot, Region: region}, nil
}
