package lifecycle

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// requireBd skips when bd is not installed. It is in the Linux dev shell (see
// flake.nix) so this runs in CI; on a Mac the derivation has no darwin entry
// and this test is skipped.
func requireBd(t *testing.T) {
	t.Helper()
	if !beads.Available() {
		t.Skip("bd not on PATH")
	}
}

// bdInitStealth makes repo a real beads repository, the same way bd's own
// tests do: a real .beads/ directory that `bd dolt remote list` can actually
// answer, rather than a bare directory that would trip Detect's Present
// check and hide what "off" is really guarding.
func bdInitStealth(t *testing.T, repo string) {
	t.Helper()
	// #nosec G204 -- test-only, fixed argv.
	cmd := exec.Command("bd", "init", "--stealth")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init --stealth in %s: %v\n%s", repo, err, out)
	}
}

// Beads must never fail a session. seedBeads returns nothing at all, so there
// is no error for a caller to accidentally propagate -- the type enforces the
// policy rather than a comment asking callers to remember it.
func TestSeedBeads_IsSilentWhenTheModeIsOff(t *testing.T) {
	requireBd(t)
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// A repository with a REAL .beads/ database, not a bare t.TempDir(): with
	// nothing there, beads.Detect returns ModeAbsent on its own and the "off"
	// check is never the reason seedBeads stops. Only a repo that would
	// otherwise sail past Detect proves the short-circuit is doing anything.
	localRepo := t.TempDir()
	initRepo(t, localRepo)
	bdInitStealth(t, localRepo)

	// A nil client would panic if anything downstream ran; "off" must return
	// before touching it.
	seedBeads(ctx, nil, localRepo, "/home/u/sessions/s/repo", "u", "h", "s", "off")

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence — \"off\" is a choice, not a problem", errOut.String())
	}
}

func TestSeedBeads_IsSilentWhenTheRepoHasNoBeads(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// t.TempDir() has no .beads/, which is the overwhelmingly common case:
	// the default mode must be completely inert there, not merely harmless.
	seedBeads(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "u", "h", "s", "session")

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence for a repository with no beads", errOut.String())
	}
}

func TestPullBeads_IsSilentWhenBeadsWasNeverWiredForTheSession(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// A session started while beads was off, or in a repository that never
	// had beads, must stay silent forever after -- pull runs on every
	// checkpoint and a warning per pull would train the user to ignore them.
	// A nil client would panic if anything downstream ran.
	if synced := pullBeads(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "s"); !synced {
		t.Error("pullBeads() = false, want true — nothing to sync is not a failed sync, " +
			"and merge decides whether to warn about losing issues from this")
	}

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence when beads was never wired", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want silence when beads was never wired", out.String())
	}
}

func TestRequireBeadsLanded_PassesWhenBeadsWasNeverWired(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// The guard is fail-closed, but "never wired" is not an unknown -- it is
	// a known nothing. Treating it as unsafe would make every session in
	// every repository without beads undeletable.
	if err := requireBeadsLanded(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "s"); err != nil {
		t.Errorf("requireBeadsLanded() error = %v, want nil when beads was never wired", err)
	}
}

// wireBeadsForRequireLanded gives localRepo a real dolt remote registered for
// session, pointed at a real (but otherwise unrelated) git repository -- the
// same fixture shape sync_test.go's TestUnpulled_* tests use, so
// requireBeadsLanded's guard can be driven end to end through a real
// beads.Unpulled instead of only through beads.Wired's short-circuit.
func wireBeadsForRequireLanded(t *testing.T, localRepo, session string) {
	t.Helper()
	initRepo(t, localRepo)
	bdInitStealth(t, localRepo)
	sessionRepo := t.TempDir()
	initRepo(t, sessionRepo)
	if err := beads.Seed(context.Background(), localRepo, session, beads.FileURL(sessionRepo)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
}

// The guard is fail-closed, which is only worth anything if it can be shown
// to actually close. A guard that always passes would make
// TestRequireBeadsLanded_PassesWhenBeadsWasNeverWired look identical to one
// that never checks anything at all -- this is what tells them apart.
func TestRequireBeadsLanded_RefusesWhenTheSyncCannotConfirmIssuesLanded(t *testing.T) {
	requireBd(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	session := "s"
	localRepo := t.TempDir()
	wireBeadsForRequireLanded(t, localRepo, session)

	// Every command the instance receives fails, so Unpulled can never
	// establish that the round trip completed.
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		return "dolt push exploded", 1
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	err = requireBeadsLanded(context.Background(), client, localRepo, "/home/devuser/sessions/s/repo", session)
	if err == nil {
		t.Fatal("requireBeadsLanded() = nil when the instance-side sync failed, want a refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to mention --force", err.Error())
	}
	if !strings.Contains(err.Error(), session) {
		t.Errorf("error = %q, want it to name session %s", err.Error(), session)
	}
}

// The other half of the same guard: once the sync actually completes, it
// must let the caller proceed rather than refusing unconditionally.
func TestRequireBeadsLanded_PassesOnceIssuesHaveLanded(t *testing.T) {
	requireBd(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	session := "s"
	localRepo := t.TempDir()
	wireBeadsForRequireLanded(t, localRepo, session)

	// Every command the instance receives succeeds; nothing changed since
	// Seed already pushed, so the local `bd dolt pull` that follows has
	// nothing new to bring home and succeeds too.
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		return "", 0
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := requireBeadsLanded(context.Background(), client, localRepo, "/home/devuser/sessions/s/repo", session); err != nil {
		t.Errorf("requireBeadsLanded() error = %v, want nil once the sync completed", err)
	}
}
