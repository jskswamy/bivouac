package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// bdArgv runs cmd (a string built by this package, e.g. from instanceCmd)
// through a real shell, with a stub `bd` on PATH that records the argument
// vector it was actually invoked with, one argument per line, into a file
// named by $BD_ARGV_FILE.
//
// This is what a substring check on the command string can't tell apart: a
// dropped argument, an argument split by a missing quote, or two arguments
// fused by a missing separator all still contain the right substrings
// somewhere in the string, but none of them produce the right argv. Reading
// argv back is the same distinction TestInstanceCommands_QuoteTheRepoPath
// draws for the repo path -- run the thing for real and observe what it
// actually did -- applied to the rest of the command line instead of the
// `cd`.
func bdArgv(t *testing.T, cmd string) []string {
	t.Helper()
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "bd")
	script := "#!/bin/sh\n: > \"$BD_ARGV_FILE\"\nfor a in \"$@\"; do\n  printf '%s\\n' \"$a\" >> \"$BD_ARGV_FILE\"\ndone\n"
	if err := os.WriteFile(stub, []byte(script), 0o750); err != nil {
		t.Fatal(err)
	}

	argvFile := filepath.Join(t.TempDir(), "argv")
	// #nosec G204 -- test-only, command built by this package.
	c := exec.Command("bash", "-c", cmd)
	c.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"BD_ARGV_FILE="+argvFile,
	)
	out, err := c.CombinedOutput()

	raw, readErr := os.ReadFile(argvFile) //nolint:gosec // test-only, path built above
	if readErr != nil {
		// The stub never ran -- most likely the login shell's own profile
		// scripts reordered PATH ahead of our prepended stub dir. A missing
		// argv file means this test proved nothing about quoting; failing
		// loudly here, rather than falling through to an empty argv slice
		// that would just report a mismatch, keeps that distinction visible.
		t.Fatalf("stub bd never ran (PATH likely reset by the login shell): cmd exit err=%v\n%s", err, out)
	}
	trimmed := strings.TrimSuffix(string(raw), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// Every instance command must run inside a login shell: bd comes from the
// instance user's home-manager profile (~/.nix-profile/bin), and a
// non-interactive SSH command runs a non-login shell that never sources the
// profile scripts putting that on PATH. Same reasoning as
// lifecycle.remoteGitCmd.
func TestInstanceCommands_RunInALoginShell(t *testing.T) {
	cmds := map[string]string{
		"init":      initCmd("/home/u/sessions/s/repo", "git+file:///home/u/sessions/s/repo"),
		"remoteAdd": remoteAddCmd("/home/u/sessions/s/repo", "dolthub", "https://doltremoteapi.dolthub.com/j/c"),
		"push":      pushCmd("/home/u/sessions/s/repo"),
		"version":   versionCmd("/home/u/sessions/s/repo"),
	}
	for name, cmd := range cmds {
		if !strings.HasPrefix(cmd, "bash -lc ") {
			t.Errorf("%s = %q, want a bash -lc wrapper", name, cmd)
		}
	}
}

func TestInitCmd_IsStealthAndCarriesTheRemote(t *testing.T) {
	// bdArgv actually executes the built command, `cd` included, so repo
	// must be a directory that exists -- unlike the fixed
	// "/home/u/sessions/s/repo" this package uses elsewhere purely as
	// string-shaped test data. The remote URL stays that fixed literal:
	// it never needs to resolve to anything, only to arrive intact.
	repo := t.TempDir()
	got := initCmd(repo, "git+file:///home/u/sessions/s/repo")
	// --stealth regardless of the Mac's mode: it keeps bd from writing its
	// own tracked files into the checkout, which a checkpoint's `git add -A`
	// would otherwise commit.
	//
	// Asserted against the actual argument vector bd receives, not a
	// substring of the built command string: every argument is shell-quoted
	// (see instanceCmd), so no unquoted "bd init" substring exists to find,
	// and a substring check can't tell a dropped or reordered argument from
	// a correct one anyway -- the exact slice can.
	want := []string{"init", "--stealth", "--remote", "git+file:///home/u/sessions/s/repo"}
	if argv := bdArgv(t, got); !slices.Equal(argv, want) {
		t.Errorf("initCmd() ran bd with argv %v, want %v", argv, want)
	}
}

func TestPushCmd_PushesFromTheSessionRepo(t *testing.T) {
	repo := t.TempDir()
	got := pushCmd(repo)
	want := []string{"dolt", "push"}
	if argv := bdArgv(t, got); !slices.Equal(argv, want) {
		t.Errorf("pushCmd() ran bd with argv %v, want %v", argv, want)
	}
	if !strings.Contains(got, repo) {
		t.Errorf("pushCmd() = %q, want it to name the repo directory", got)
	}
}

// A path with a space is not hypothetical -- the repo name comes from the
// user's own directory name -- and an unquoted one would split into two
// arguments inside the login shell. Asserted by running the command through a
// real shell and checking which command failed, because string-matching a
// nest of quotes tests the assertion's own cleverness rather than the
// command.
//
// Piping the built command's own output to an outer `pwd` cannot show this:
// `bash -lc '...'` is a child process, and a `cd` inside it never changes the
// working directory of the shell that spawned it, correctly quoted or not.
// The distinction that is actually observable from outside is which command
// fails: `bd` need not exist for this -- a correctly quoted path lets `cd`
// succeed, so any failure that follows is bd's own (command not found)
// rather than cd's, while a mis-quoted path splits on the space and fails at
// the cd itself, with bash naming `cd` in the error.
func TestInstanceCommands_QuoteTheRepoPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my repo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	got := instanceCmd(dir, "version")
	// #nosec G204 -- test-only, command built by this package.
	out, err := exec.Command("bash", "-c", got).CombinedOutput()
	if err != nil && strings.Contains(string(out), "cd:") {
		t.Errorf("instanceCmd(%q) mis-quoted the repo path, cd failed:\n%s", dir, out)
	}
}
