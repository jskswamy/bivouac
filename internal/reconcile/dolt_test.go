package reconcile

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

func TestPlaceDoltCredential_DoesNothingOutsideDolthubMode(t *testing.T) {
	for _, mode := range []string{"session", "off", ""} {
		t.Run(mode, func(t *testing.T) {
			var out, errOut bytes.Buffer
			ctx := provider.WithOutput(context.Background(), &out, &errOut)
			// A nil client would panic if anything ran: the mode check must
			// come before any connection use at all.
			placeDoltCredential(ctx, nil, mode)
			if errOut.Len() != 0 {
				t.Errorf("errOut = %q, want silence in %q mode", errOut.String(), mode)
			}
		})
	}
}

func TestPlaceDoltCredential_WarnsAndContinuesWhenTheSecretIsMissing(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// XDG_CONFIG_HOME at an empty temp dir means secrets.Path() names a file
	// that does not exist, which is exactly the "asked for dolthub, never ran
	// secrets init" case.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	placeDoltCredential(ctx, nil, "dolthub")

	got := errOut.String()
	if !strings.Contains(got, "dolthub_creds") {
		t.Errorf("errOut = %q, want it to name the missing key", got)
	}
	if !strings.Contains(got, "session") {
		t.Errorf("errOut = %q, want it to say sync falls back to session mode", got)
	}
}

// runDoltPrepareScript executes the real script doltPrepareScript builds,
// against a scratch $HOME, exactly as the instance would run it (minus the
// SSH hop) -- this is a real bash process, not a simulation of one, so it
// proves the shell logic itself rather than a Go re-implementation of it.
func runDoltPrepareScript(t *testing.T, home, doltDir string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", doltPrepareScript(doltDir))
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "NOTASYMLINK") {
		t.Fatalf("doltPrepareScript exited %v, output = %q", err, out)
	}
	return string(out)
}

func TestDoltPrepareScript_LeavesAnythingThatIsNotOurOwnSymlinkAlone(t *testing.T) {
	doltDir := filepath.Join(t.TempDir(), "cloudlab", "dolt")
	dotDoltOf := func(home string) string { return filepath.Join(home, ".dolt") }

	t.Run("absent: creates the symlink", func(t *testing.T) {
		home := t.TempDir()
		out := runDoltPrepareScript(t, home, doltDir)
		if strings.Contains(out, "NOTASYMLINK") {
			t.Fatalf("output = %q, want no NOTASYMLINK for an absent ~/.dolt", out)
		}
		target, err := os.Readlink(dotDoltOf(home))
		if err != nil {
			t.Fatalf("Readlink(~/.dolt) error = %v, want a symlink to have been created", err)
		}
		if target != doltDir {
			t.Errorf("~/.dolt -> %q, want %q", target, doltDir)
		}
	})

	t.Run("already our symlink: left pointing at doltDir", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Symlink(doltDir, dotDoltOf(home)); err != nil {
			t.Fatal(err)
		}
		out := runDoltPrepareScript(t, home, doltDir)
		if strings.Contains(out, "NOTASYMLINK") {
			t.Fatalf("output = %q, want no NOTASYMLINK when ~/.dolt already points at doltDir", out)
		}
		target, err := os.Readlink(dotDoltOf(home))
		if err != nil {
			t.Fatalf("Readlink(~/.dolt) error = %v", err)
		}
		if target != doltDir {
			t.Errorf("~/.dolt -> %q, want it to still point at %q", target, doltDir)
		}
	})

	t.Run("real directory: left alone", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(dotDoltOf(home), 0o700); err != nil {
			t.Fatal(err)
		}
		out := runDoltPrepareScript(t, home, doltDir)
		if !strings.Contains(out, "NOTASYMLINK") {
			t.Fatalf("output = %q, want NOTASYMLINK for a real directory", out)
		}
		info, err := os.Lstat(dotDoltOf(home))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Error("~/.dolt was turned into a symlink, want the real directory left untouched")
		}
	})

	t.Run("foreign live symlink: left pointing at its own target", func(t *testing.T) {
		home := t.TempDir()
		foreignTarget := t.TempDir()
		if err := os.Symlink(foreignTarget, dotDoltOf(home)); err != nil {
			t.Fatal(err)
		}
		out := runDoltPrepareScript(t, home, doltDir)
		if !strings.Contains(out, "NOTASYMLINK") {
			t.Fatalf("output = %q, want NOTASYMLINK for a symlink to somewhere else", out)
		}
		target, err := os.Readlink(dotDoltOf(home))
		if err != nil {
			t.Fatal(err)
		}
		if target != foreignTarget {
			t.Errorf("~/.dolt -> %q, want it untouched at %q", target, foreignTarget)
		}
	})

	t.Run("dangling symlink: left pointing at its own (missing) target", func(t *testing.T) {
		home := t.TempDir()
		danglingTarget := filepath.Join(home, "nonexistent-target-for-test")
		if err := os.Symlink(danglingTarget, dotDoltOf(home)); err != nil {
			t.Fatal(err)
		}
		out := runDoltPrepareScript(t, home, doltDir)
		if !strings.Contains(out, "NOTASYMLINK") {
			t.Fatalf("output = %q, want NOTASYMLINK for a dangling symlink", out)
		}
		target, err := os.Readlink(dotDoltOf(home))
		if err != nil {
			t.Fatal(err)
		}
		if target != danglingTarget {
			t.Errorf("~/.dolt -> %q, want it untouched at %q", target, danglingTarget)
		}
	})

	t.Run("regular file: left alone", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(dotDoltOf(home), []byte("not cloudlab's"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runDoltPrepareScript(t, home, doltDir)
		if !strings.Contains(out, "NOTASYMLINK") {
			t.Fatalf("output = %q, want NOTASYMLINK for a regular file", out)
		}
		content, err := os.ReadFile(dotDoltOf(home))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "not cloudlab's" {
			t.Errorf("~/.dolt content = %q, want it untouched", content)
		}
	})
}

func TestSanitizeDoltCredsID_RejectsPathTraversalAndEmptyAcceptsPlainTokens(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"typical id", "us8isfuv1a2b3c", false},
		{"mixed case, dash, underscore", "abc_DEF-123", false},
		{"empty", "", true},
		{"parent traversal", "../../etc/passwd", true},
		{"embedded slash", "creds/../../etc", true},
		{"embedded space", "foo bar", true},
		{"leading dash", "-rf", true},
		{"bare dash", "-", true},
		{"double dash", "--", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := sanitizeDoltCredsID(c.id)
			if (err != nil) != c.wantErr {
				t.Errorf("sanitizeDoltCredsID(%q) error = %v, wantErr %v", c.id, err, c.wantErr)
			}
		})
	}
}
