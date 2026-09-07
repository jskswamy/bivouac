package lifecycle

import (
	"bytes"
	"context"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// Beads must never fail a session. seedBeads returns nothing at all, so there
// is no error for a caller to accidentally propagate -- the type enforces the
// policy rather than a comment asking callers to remember it.
func TestSeedBeads_IsSilentWhenTheModeIsOff(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// A nil client would panic if anything downstream ran; "off" must return
	// before touching it.
	seedBeads(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "u", "h", "s", "off")

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
