package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeToken = "dop_v1_supplied-token"

// installFakePacker puts testdata/fakepacker.sh first on PATH as packer
// and returns the bundle dir to run it in and the log it writes.
func installFakePacker(t *testing.T) (dir, log string) {
	t.Helper()
	script, err := os.ReadFile("testdata/fakepacker.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "packer"), script, 0o700); err != nil { // #nosec G306 -- the fake must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log = filepath.Join(t.TempDir(), "packer.log")
	t.Setenv("FAKEPACKER_LOG", log)
	t.Setenv("FAKEPACKER_FAIL", "")
	t.Setenv("FAKEPACKER_HANG", "")
	t.Setenv("FAKEPACKER_MANIFEST", manifestJSON("blr1:123456"))
	return t.TempDir(), log
}

func manifestJSON(ids ...string) string {
	var builds []string
	for i, id := range ids {
		builds = append(builds, `{"name":"image","builder_type":"digitalocean","artifact_id":"`+id+`","packer_run_uuid":"run-`+string(rune('a'+i))+`"}`)
	}
	return `{"builds":[` + strings.Join(builds, ",") + `],"last_run_uuid":"run-z"}`
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log) // #nosec G304 -- a path under t.TempDir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

func argvLines(log string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if rest, ok := strings.CutPrefix(line, "argv: "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func asOpError(t *testing.T, err error) *OpError {
	t.Helper()
	var op *OpError
	if !errors.As(err, &op) {
		t.Fatalf("error = %v (%T), want an *OpError", err, err)
	}
	return op
}

func TestRunPacker_MissingPackerSaysHowToInstallIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := RunPacker(context.Background(), t.TempDir(), fakeToken, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "developer.hashicorp.com/packer/install") {
		t.Errorf("error = %v, want it to point at the Packer install page", err)
	}
}

func TestRunPacker_InitsValidatesThenBuildsAndReturnsTheArtifact(t *testing.T) {
	dir, log := installFakePacker(t)

	got, err := RunPacker(context.Background(), dir, fakeToken, &strings.Builder{})
	if err != nil {
		t.Fatalf("RunPacker() error = %v", err)
	}
	if want := (Artifact{ID: "123456", Region: "blr1"}); got != want {
		t.Errorf("RunPacker() = %+v, want %+v", got, want)
	}
	calls := argvLines(readLog(t, log))
	if want := []string{"init .", "validate .", "build ."}; strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("packer calls = %q, want %q", calls, want)
	}
}

func TestRunPacker_ValidateFailureStopsBeforeBuild(t *testing.T) {
	dir, log := installFakePacker(t)
	t.Setenv("FAKEPACKER_FAIL", "validate")

	_, err := RunPacker(context.Background(), dir, fakeToken, &strings.Builder{})
	if op := asOpError(t, err); op.Phase != "packer-validate" || op.ImageCreated {
		t.Errorf("OpError = %+v, want phase packer-validate with no image", op)
	}
	if strings.Contains(readLog(t, log), "argv: build") {
		t.Error("build ran after validate failed")
	}
}

// The token reaches Packer through its environment and nowhere else: not
// argv, where ps shows it, and not the progress a caller may log.
func TestRunPacker_TokenIsOnlyInTheChildEnvironment(t *testing.T) {
	dir, log := installFakePacker(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "stale-token-from-the-shell")
	t.Setenv("PKR_VAR_do_token", "stale-token-as-a-packer-var")
	var progress strings.Builder

	if _, err := RunPacker(context.Background(), dir, fakeToken, &progress); err != nil {
		t.Fatalf("RunPacker() error = %v", err)
	}
	logged := readLog(t, log)
	if n := strings.Count(logged, "token: "+fakeToken+"\n"); n != 3 {
		t.Errorf("log = %q, want the supplied token in all three calls' env", logged)
	}
	if strings.Contains(logged, "stale-token") {
		t.Errorf("log = %q, want the inherited token replaced, not kept alongside", logged)
	}
	if strings.Count(logged, "pkr_var: unset\n") != 3 {
		t.Errorf("log = %q, want PKR_VAR_do_token removed, since Packer prefers it", logged)
	}
	for _, argv := range argvLines(logged) {
		if strings.Contains(argv, fakeToken) {
			t.Errorf("argv %q carries the token", argv)
		}
	}
	if !strings.Contains(progress.String(), "fake packer build on stderr") {
		t.Errorf("progress = %q, want Packer's stdout and stderr streamed to it", progress.String())
	}
	if strings.Contains(progress.String(), fakeToken) {
		t.Error("progress output carries the token")
	}
}

func TestRunPacker_CancelReportsPossibleLeftovers(t *testing.T) {
	dir, log := installFakePacker(t)
	t.Setenv("FAKEPACKER_HANG", "build")
	t.Setenv("FAKEPACKER_MANIFEST", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := RunPacker(ctx, dir, fakeToken, &strings.Builder{})
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(readLog(t, log), "hanging: build") {
		if time.Now().After(deadline) {
			t.Fatal("fake packer build never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	op := asOpError(t, err)
	if op.Phase != "packer-build" {
		t.Errorf("phase = %q, want packer-build", op.Phase)
	}
	if !strings.Contains(op.Cleanup, "builder droplet") {
		t.Errorf("cleanup = %q, want it to warn of a possible leftover builder droplet", op.Cleanup)
	}
	if !strings.Contains(readLog(t, log), "interrupted: build") {
		t.Error("packer was not interrupted, so it had no chance to clean up")
	}
}

func TestRunPacker_MissingManifestAfterSuccessIsAnError(t *testing.T) {
	dir, _ := installFakePacker(t)
	t.Setenv("FAKEPACKER_MANIFEST", "")

	_, err := RunPacker(context.Background(), dir, fakeToken, &strings.Builder{})
	if op := asOpError(t, err); op.Phase != "manifest" {
		t.Errorf("phase = %q, want manifest", op.Phase)
	}
}

// The manifest post-processor appends, so a manifest left by an earlier
// run must not be read as this run's artifact.
func TestRunPacker_StaleManifestIsNotThisRunsArtifact(t *testing.T) {
	dir, _ := installFakePacker(t)
	t.Setenv("FAKEPACKER_MANIFEST", "")
	if err := os.WriteFile(filepath.Join(dir, "packer-manifest.json"), []byte(manifestJSON("blr1:999")), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := RunPacker(context.Background(), dir, fakeToken, &strings.Builder{})
	if op := asOpError(t, err); op.Phase != "manifest" || op.ImageID != "" {
		t.Errorf("OpError = %+v, want a manifest error naming no image", op)
	}
}

func TestRunPacker_BuildFailureAfterSnapshotCarriesTheID(t *testing.T) {
	dir, _ := installFakePacker(t)
	t.Setenv("FAKEPACKER_FAIL", "build")

	_, err := RunPacker(context.Background(), dir, fakeToken, &strings.Builder{})
	op := asOpError(t, err)
	if op.Phase != "packer-build" || !op.ImageCreated || op.ImageID != "123456" {
		t.Errorf("OpError = %+v, want packer-build with image 123456 created", op)
	}
}

func TestParseManifest(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Artifact
		wantErr bool
	}{
		{"single build", manifestJSON("nyc3:123456"), Artifact{ID: "123456", Region: "nyc3"}, false},
		{"last of two builds", manifestJSON("blr1:111", "nyc3:222"), Artifact{ID: "222", Region: "nyc3"}, false},
		{"malformed json", `{"builds":[`, Artifact{}, true},
		{"no builds", `{"builds":[]}`, Artifact{}, true},
		{"no region", manifestJSON("123456"), Artifact{}, true},
		{"non-numeric id", manifestJSON("nyc3:abc"), Artifact{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseManifest([]byte(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseManifest() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestOpError_UnwrapsAndNamesThePhase(t *testing.T) {
	err := &OpError{Phase: "packer-build", ImageCreated: true, ImageID: "123", Err: context.Canceled}
	if !errors.Is(err, context.Canceled) {
		t.Error("errors.Is(OpError, Canceled) = false, want the cause reachable")
	}
	if msg := err.Error(); !strings.Contains(msg, "packer-build") || !strings.Contains(msg, "123") {
		t.Errorf("Error() = %q, want the phase and the known image ID", msg)
	}
}
