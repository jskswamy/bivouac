package beads

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// fakeRunner is the test double for instanceRunner. It records, in order,
// every command it received, and can be told to fail on one specific call by
// index. This is what lets Bootstrap, Pull and Unpulled -- otherwise only
// exercisable against a live SSH connection -- be driven and asserted on
// without one.
type fakeRunner struct {
	commands []string
	failAt   int // index into commands to fail on; -1 means never fail
	failErr  error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{failAt: -1}
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	i := len(f.commands)
	f.commands = append(f.commands, cmd)
	if i == f.failAt {
		return "boom", f.failErr
	}
	return "ok", nil
}

func TestBootstrap_SessionModeIssuesOnlyInit(t *testing.T) {
	fr := newFakeRunner()
	repo, fileURL := "/repo", "git+file:///session-repo"

	if err := Bootstrap(fr, repo, fileURL, ""); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	want := []string{initCmd(repo, fileURL)}
	if !slices.Equal(fr.commands, want) {
		t.Fatalf("commands = %v, want %v (init only, no external remote)", fr.commands, want)
	}
}

func TestBootstrap_DolthubModeIssuesInitThenRemoteAdd(t *testing.T) {
	fr := newFakeRunner()
	repo, fileURL, externalURL := "/repo", "git+file:///session-repo", "https://doltremoteapi.dolthub.com/x/y"

	if err := Bootstrap(fr, repo, fileURL, externalURL); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	want := []string{
		initCmd(repo, fileURL),
		remoteAddCmd(repo, "dolthub", externalURL),
	}
	if !slices.Equal(fr.commands, want) {
		t.Fatalf("commands = %v, want %v (init, then the external remote add, in order)", fr.commands, want)
	}
}

func TestBootstrap_SurfacesAnInitFailureWithoutAddingTheRemote(t *testing.T) {
	fr := newFakeRunner()
	fr.failAt = 0
	fr.failErr = errors.New("init exploded")
	repo, fileURL, externalURL := "/repo", "git+file:///session-repo", "https://doltremoteapi.dolthub.com/x/y"

	err := Bootstrap(fr, repo, fileURL, externalURL)
	if err == nil {
		t.Fatal("Bootstrap() error = nil, want the init failure")
	}

	want := []string{initCmd(repo, fileURL)}
	if !slices.Equal(fr.commands, want) {
		t.Fatalf("commands = %v, want %v (init only -- the remote add must not run after init fails)", fr.commands, want)
	}
}

func TestUnpulled_ReturnsFalseOnlyOnFullSuccess(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")
	initGitRepo(t, mac)
	initGitRepo(t, session)
	mustRun(t, mac, "bd", "init", "--stealth")
	mustRun(t, mac, "bd", "create", "Read me on the instance")

	if err := Seed(t.Context(), mac, "fix-auth", FileURL(session)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	fr := newFakeRunner()
	unpulled, err := Unpulled(t.Context(), mac, "fix-auth", fr, "/repo")
	if err != nil {
		t.Fatalf("Unpulled() error = %v, want nil", err)
	}
	if unpulled {
		t.Error("Unpulled() = true, want false after a successful sync")
	}

	want := []string{pushCmd("/repo")}
	if !slices.Equal(fr.commands, want) {
		t.Fatalf("instance commands = %v, want %v (the instance-side push must actually run)", fr.commands, want)
	}
}

func TestUnpulled_ReturnsTrueAndErrorWhenTheInstancePushFails(t *testing.T) {
	fr := newFakeRunner()
	fr.failAt = 0
	fr.failErr = errors.New("ssh connection reset")

	// localRepo is never touched: Pull returns before reaching the local
	// `bd dolt pull`, and the fake's single recorded command below proves it.
	unpulled, err := Unpulled(t.Context(), "/does-not-matter", "fix-auth", fr, "/repo")
	if !unpulled {
		t.Error("Unpulled() bool = false, want true when the instance push fails")
	}
	if err == nil {
		t.Fatal("Unpulled() error = nil, want the push failure -- a false negative here would authorise destroying issue work")
	}

	want := []string{pushCmd("/repo")}
	if !slices.Equal(fr.commands, want) {
		t.Fatalf("instance commands = %v, want %v (only the push, never the local pull)", fr.commands, want)
	}
}

// TestUnpulled_ReturnsTrueAndErrorWhenTheLocalPullFails exercises the leg
// Pull runs after the instance push succeeds: `run(ctx, localRepo,
// pullArgs(...)...)`, a direct exec.Command call to the real bd binary. That
// call isn't behind instanceRunner -- only the instance side is -- so it
// can't be faked here; instead this drives it with a localRepo that has no
// beads database at all, which makes the real `bd dolt pull` fail
// immediately and for a reason having nothing to do with the instance side.
func TestUnpulled_ReturnsTrueAndErrorWhenTheLocalPullFails(t *testing.T) {
	requireBd(t)

	localRepo := t.TempDir() // no `bd init` here: pull has nothing to pull into

	fr := newFakeRunner()
	unpulled, err := Unpulled(t.Context(), localRepo, "fix-auth", fr, "/repo")
	if !unpulled {
		t.Error("Unpulled() bool = false, want true when the local pull fails")
	}
	if err == nil {
		t.Fatal("Unpulled() error = nil, want the local pull failure")
	}

	want := []string{pushCmd("/repo")}
	if !slices.Equal(fr.commands, want) {
		t.Fatalf("instance commands = %v, want %v (the instance push must still have run first)", fr.commands, want)
	}
}
