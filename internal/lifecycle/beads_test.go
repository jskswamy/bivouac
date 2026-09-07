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

// A warned-and-continued Bootstrap failure must not leave the Mac's dolt
// remote in place: Wired keys entirely on that remote, so a session with no
// .beads/ on the instance at all would otherwise read as "wired" and every
// later requireBeadsLanded call would run `bd dolt push` against an instance
// that has no beads database, refusing session delete and `cloudlab down`
// forever. This is the default upgrade path -- any instance provisioned
// before beads shipped has no `bd` binary, and Bootstrap's "command not
// found" is exactly the failure this guards.
//
// Exercises bootstrapBeads directly rather than seedBeads end to end: seeding
// through a real ssh:// dolt remote needs a real git/dolt-speaking SSH
// server, which the fake one here cannot provide (see
// TestSeedSession_CreatesTheRepoThenPushes's own comment on the same
// limitation -- "the fake SSH server cannot complete a real git push").
// wireBeadsForRequireLanded already establishes the pattern of wiring the
// remote over a real file:// remote and driving only the instance-side half
// through the fake server; this reuses it.
func TestBootstrapBeads_RemovesTheRemoteWhenBootstrapFails(t *testing.T) {
	requireBd(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	session := "s"
	localRepo := t.TempDir()
	wireBeadsForRequireLanded(t, localRepo, session)

	// Every command containing "bd init" fails, standing in for an instance
	// with no bd binary on PATH.
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		if strings.Contains(cmd, "bd") && strings.Contains(cmd, "init") {
			return "bash: bd: command not found", 127
		}
		return "", 0
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	bootstrapBeads(ctx, client, localRepo, "/home/devuser/sessions/s/repo", session, "")

	if beads.Wired(ctx, localRepo, session) {
		t.Error("Wired() = true after a failed Bootstrap, want false -- a warned-and-continued " +
			"setup failure must be a known nothing, not something the fail-closed delete/down guard trips on")
	}
}

// A Bootstrap failure in the external "dolthub" remote-add step, after `bd
// init` already succeeded, leaves the instance with a perfectly good
// database -- the Mac's dolt remote must survive that, unlike an init
// failure. Removing it here would silently drop the account-wide DoltHub
// sync protection the user paid a credential for: Wired would go false,
// pullBeads would report "nothing to sync", and requireBeadsLanded would
// pass, all without a word, while the session's own git+ssh transport (and
// the agent's issue edits riding it) is completely intact.
func TestBootstrapBeads_KeepsTheRemoteWhenOnlyTheExternalRemoteAddFails(t *testing.T) {
	requireBd(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	session := "s"
	localRepo := t.TempDir()
	wireBeadsForRequireLanded(t, localRepo, session)

	// bd init succeeds; only the dolthub remote-add fails, standing in for
	// an instance that has no network reachability to DoltHub or a bad
	// credential -- either way, `bd init` already produced a real database.
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		if strings.Contains(cmd, "remote") && strings.Contains(cmd, "add") && strings.Contains(cmd, "dolthub") {
			return "could not reach doltremoteapi.dolthub.com", 1
		}
		return "", 0
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)
	bootstrapBeads(ctx, client, localRepo, "/home/devuser/sessions/s/repo", session, "https://doltremoteapi.dolthub.com/x/y")

	if !beads.Wired(ctx, localRepo, session) {
		t.Error("Wired() = false after only the external remote-add failed, want true -- " +
			"bd init succeeded, the instance has a real database, and the session's own " +
			"git+ssh transport still carries issues home")
	}
	if !strings.Contains(errOut.String(), "external dolt remote") {
		t.Errorf("errOut = %q, want a warning naming the external remote-add failure", errOut.String())
	}
}

// The warning must say what happened -- silence here is exactly the failure
// mode Fix 1 exists to close.
func TestBootstrapBeads_WarnsWhenBootstrapFails(t *testing.T) {
	requireBd(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	session := "s"
	localRepo := t.TempDir()
	wireBeadsForRequireLanded(t, localRepo, session)

	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		if strings.Contains(cmd, "bd") && strings.Contains(cmd, "init") {
			return "bash: bd: command not found", 127
		}
		return "", 0
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)
	bootstrapBeads(ctx, client, localRepo, "/home/devuser/sessions/s/repo", session, "")

	if !strings.Contains(errOut.String(), "command not found") {
		t.Errorf("errOut = %q, want it to carry the Bootstrap failure", errOut.String())
	}
}

// beads = "dolthub" ships a credential only from a repository already in
// ModeExternal (see seedBeads' own "external" computation); asking for it
// anywhere else silently gets the session remote and nothing more. The user
// asked for external sync and did not get it, and that must not be silent.
func TestWarnDolthubWithoutExternalRemote_WarnsWhenTheRepositoryIsNotExternal(t *testing.T) {
	for _, mode := range []beads.Mode{beads.ModeUnsynced, beads.ModeGit} {
		t.Run(mode.String(), func(t *testing.T) {
			var out, errOut bytes.Buffer
			ctx := provider.WithOutput(context.Background(), &out, &errOut)

			warnDolthubWithoutExternalRemote(ctx, "dolthub", beads.Detection{Mode: mode})

			if !strings.Contains(errOut.String(), "dolthub") {
				t.Errorf("errOut = %q, want it to name dolthub mode", errOut.String())
			}
		})
	}
}

// The other half: a caller weighing whether this warning fires at all needs
// both the "asked for dolthub, didn't get it" case above and this "got
// exactly what was asked for, or didn't ask" case to tell a real gate apart
// from one that always warns.
func TestWarnDolthubWithoutExternalRemote_SilentWhenModeMatchesTheRequest(t *testing.T) {
	tests := []struct {
		name      string
		beadsMode string
		detected  beads.Detection
	}{
		{"session mode never checks the repository's sync target", "session", beads.Detection{Mode: beads.ModeGit}},
		{"dolthub mode with an external remote to reuse", "dolthub", beads.Detection{Mode: beads.ModeExternal, ExternalURL: "https://doltremoteapi.dolthub.com/x/y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			ctx := provider.WithOutput(context.Background(), &out, &errOut)

			warnDolthubWithoutExternalRemote(ctx, tt.beadsMode, tt.detected)

			if errOut.Len() != 0 {
				t.Errorf("errOut = %q, want silence", errOut.String())
			}
		})
	}
}

// delete.go and down.go both wrapped their beads guard as
// `if client, err := reconcile.Connect(...); err == nil { ... }` -- so a
// failed Connect skipped the issue check with no warning at all, an unknown
// treated as safe inside the one guard whose contract is the opposite.
// checkBeadsLanded is the shared call site both verbs now use; this proves
// its Connect failure is never silent.
func TestCheckBeadsLanded_WarnsRatherThanSkipsSilentlyWhenConnectFails(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// Nothing listens here; connection is refused immediately.
	unreachable := "127.0.0.1:1"

	err := checkBeadsLanded(ctx, unreachable, "devuser", t.TempDir(), "/home/devuser/sessions/s/repo", "s")
	if err != nil {
		t.Errorf("checkBeadsLanded() error = %v, want nil -- RescueSession already runs (and fails loudly) "+
			"immediately above every call site, so this stays best-effort", err)
	}
	if errOut.Len() == 0 {
		t.Fatal("errOut is empty, want a warning naming the skipped issue check")
	}
	if !strings.Contains(errOut.String(), "session s's issues have landed") {
		t.Errorf("errOut = %q, want it to name session s and say its issues may not have landed", errOut.String())
	}
}

// The other half: once Connect succeeds, checkBeadsLanded must actually run
// the guard rather than always passing -- otherwise the warning test above
// could be "explained" by a function that never does anything at all.
func TestCheckBeadsLanded_RunsTheGuardWhenConnectSucceeds(t *testing.T) {
	startFakeAgent(t)
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		return "", 0
	})

	// beads was never wired in this empty repo, so the guard passes -- but it
	// had to actually run requireBeadsLanded to reach that answer, since a
	// stub returning nil unconditionally would look identical here.
	if err := checkBeadsLanded(context.Background(), addr, "devuser", t.TempDir(), "/home/devuser/sessions/s/repo", "s"); err != nil {
		t.Errorf("checkBeadsLanded() error = %v, want nil when beads was never wired", err)
	}
}
