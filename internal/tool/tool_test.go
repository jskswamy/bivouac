package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequire_ReturnsThePathOfAToolThatExists(t *testing.T) {
	// A binary that is on PATH everywhere this suite runs, including the
	// devshell -- checkRsync needs the path Require hands back, so "it was
	// found" is not the whole contract.
	path, err := Require("sh")
	if err != nil {
		t.Fatalf("Require(sh) error = %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("Require(sh) = %q, want an absolute path", path)
	}
}

// The drift this table exists to stop: every binary cloudlab depends on gets
// a message naming it, and the ones the devshell supplies say so. sops is
// the case that motivated it -- it used to get a bare "not found on PATH"
// while pkl and nix, from the same devshell, explained themselves.
func TestRequire_ExplainsHowToGetEachKnownTool(t *testing.T) {
	for bin, desc := range known {
		t.Run(bin, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())

			_, err := Require(bin)
			if err == nil {
				t.Fatalf("Require(%s) error = nil, want it to report the binary missing", bin)
			}
			if !strings.Contains(err.Error(), desc.name+" not found on PATH") {
				t.Errorf("error = %q, want it to name %q", err, desc.name)
			}
			if desc.hint != "" && !strings.Contains(err.Error(), desc.hint) {
				t.Errorf("error = %q, want it to carry the hint %q", err, desc.hint)
			}
		})
	}
}

// Every binary the devshell provides should point at the devshell. This is
// the assertion that would have caught sops drifting away from pkl and nix.
func TestRequire_DevshellToolsNameTheDevshell(t *testing.T) {
	for _, bin := range []string{"pkl", "nix", "sops", "rsync"} {
		if hint := known[bin].hint; !strings.Contains(hint, "nix develop") {
			t.Errorf("%s's hint = %q, want it to name the devshell that supplies it", bin, hint)
		}
	}
}

func TestRequire_UnknownToolStillReportsItByName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Require("definitely-not-a-real-binary")
	if err == nil || !strings.Contains(err.Error(), "definitely-not-a-real-binary not found on PATH") {
		t.Errorf("error = %v, want it to name the binary it could not find", err)
	}
}

func TestRun_CapturesStdoutFromTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := Run(context.Background(), dir, "ls")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(out) != "marker" {
		t.Errorf("Run() = %q, want the listing of dir, not of the test's cwd", out)
	}
}

// The reason Run and RunCombined are two functions. Run's callers parse the
// result as a path or a URL, so a tool that chatters on stderr must not be
// able to corrupt it.
func TestRun_LeavesStderrOutOfTheResult(t *testing.T) {
	out, err := Run(context.Background(), "", "sh", "-c", "echo noise >&2; echo value")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(out) != "value" {
		t.Errorf("Run() = %q, want stdout only", out)
	}
}

func TestRunCombined_KeepsStderrBecauseItIsTheDiagnostic(t *testing.T) {
	out, err := RunCombined(context.Background(), "", "sh", "-c", "echo noise >&2; echo value; exit 3")
	if err == nil {
		t.Fatal("RunCombined() error = nil, want the non-zero exit reported")
	}
	if !strings.Contains(out, "noise") {
		t.Errorf("RunCombined() = %q, want it to include what the tool said on stderr", out)
	}
}

func TestRun_ReportsAFailingToolAsAnError(t *testing.T) {
	if _, err := Run(context.Background(), "", "sh", "-c", "exit 1"); err == nil {
		t.Error("Run() error = nil, want a non-zero exit reported as an error")
	}
}

// Cancellation reaches the child. This is what threading a context into
// identity's git calls buys, so it is worth pinning rather than assuming.
func TestRun_HonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Run(ctx, "", "sleep", "30"); err == nil {
		t.Error("Run() error = nil, want the cancelled context to stop the command")
	}
}
