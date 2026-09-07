package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireBd skips when bd is not installed. It is in the Linux dev shell (see
// flake.nix) so this runs in CI; on a Mac the derivation has no darwin entry
// and this test is skipped.
func requireBd(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("bd not on PATH")
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	// #nosec G204 -- test-only, all arguments built by this test.
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// initGitRepo makes a repository with one commit on main. A branch and a
// commit are both required before dolt data can be pushed into it: pushing to
// a git remote with no branches fails with "git remote has no branches ...
// initialize the repository with an initial branch/commit first".
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "init", "--quiet")
	mustRun(t, dir, "git", "checkout", "--quiet", "-b", "main")
	mustRun(t, dir, "git", "config", "user.email", "t@example.com")
	mustRun(t, dir, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "first")
}

// TestRoundTrip_IssuesTravelToTheSessionRepoAndBack is the design's central
// claim, exercised end to end: the Mac seeds its issue database into the
// session repository's refs/dolt/data, the instance bootstraps from that same
// repository over git+file://, an agent files an issue there, and the Mac
// pulls it home.
func TestRoundTrip_IssuesTravelToTheSessionRepoAndBack(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")

	initGitRepo(t, mac)
	initGitRepo(t, session)

	// The Mac's own beads database, with one issue in it.
	mustRun(t, mac, "bd", "init", "--stealth")
	mustRun(t, mac, "bd", "create", "Read me on the instance")

	// Seed: register the session remote and push the database into it.
	url := FileURL(session)
	if err := Seed(t.Context(), mac, "fix-auth", url); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	refs := mustRun(t, session, "git", "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/dolt/") {
		t.Fatalf("session repo has no dolt refs after Seed:\n%s", refs)
	}

	// Bootstrap the instance side from that same repository, then file an
	// issue as the agent would and push it back into the ref.
	mustRun(t, session, "bd", "init", "--stealth", "--remote", FileURL(session))
	mustRun(t, session, "bd", "create", "Filed by the agent")
	mustRun(t, session, "bd", "dolt", "push")

	// Pull it home.
	if _, err := run(t.Context(), mac, pullArgs(RemoteName("fix-auth"))...); err != nil {
		t.Fatalf("bd dolt pull: %v", err)
	}
	list := mustRun(t, mac, "bd", "list")
	if !strings.Contains(list, "Filed by the agent") {
		t.Fatalf("the agent's issue did not come home:\n%s", list)
	}
}

func TestWired_ReportsWhetherTheSessionRemoteExists(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")
	initGitRepo(t, mac)
	initGitRepo(t, session)
	mustRun(t, mac, "bd", "init", "--stealth")

	if Wired(t.Context(), mac, "fix-auth") {
		t.Error("Wired() = true before Seed, want false")
	}
	if err := Seed(t.Context(), mac, "fix-auth", FileURL(session)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	if !Wired(t.Context(), mac, "fix-auth") {
		t.Error("Wired() = false after Seed, want true")
	}
}

// The regression the spec names: a dolt remote added while an external
// sync.remote is configured must resolve to a git+ URL, not one prefixed with
// the external base URL.
func TestSeed_KeepsTheGitSchemeWhenAnExternalRemoteIsConfigured(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")
	initGitRepo(t, mac)
	initGitRepo(t, session)
	mustRun(t, mac, "bd", "init", "--stealth")
	mustRun(t, mac, "bd", "dolt", "remote", "add", "origin",
		"https://doltremoteapi.dolthub.com/example/example")

	if err := Seed(t.Context(), mac, "fix-auth", FileURL(session)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	out, err := run(t.Context(), mac, remoteListArgs()...)
	if err != nil {
		t.Fatalf("bd dolt remote list: %v\n%s", err, out)
	}
	for _, r := range parseRemoteList(out) {
		if r.Name != RemoteName("fix-auth") {
			continue
		}
		if !strings.HasPrefix(r.URL, "git+file://") {
			t.Fatalf("session remote resolved to %q, want a git+file:// URL — "+
				"bd appended the path to the external base URL", r.URL)
		}
		return
	}
	t.Fatalf("no remote named %s after Seed:\n%s", RemoteName("fix-auth"), out)
}
