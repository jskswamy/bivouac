package lifecycle

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/provider"
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
