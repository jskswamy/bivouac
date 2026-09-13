package cmd

import (
	"os"
	"testing"

	"github.com/jskswamy/cloudlab/internal/testenv"
)

// Every test in this package starts with an isolated home.
//
// resolveToken falls back to the personal secrets file whenever
// DIGITALOCEAN_TOKEN is empty, so without this a test that clears the token
// -- or simply runs somewhere it was never exported -- reads the developer's
// own ~/.config/cloudlab/secrets.yaml. With a YubiKey-backed age identity
// that does not fail, it blocks: `go test ./cmd/` sits waiting for a touch
// with no output explaining why.
//
// Done package-wide on purpose. Relying on each test to remember is the
// ambient-config leak that made TestVerifySignatures_RejectsUnsignedCommits
// fail on the maintainer's machine and pass everywhere else (cloudlab-9pk),
// and that later filled a real ~/.ssh/known_hosts with 190 stale entries
// (cloudlab-1o0). Twice is enough to stop hand-rolling it per package, which
// is why the mechanics live in internal/testenv now. Tests that want a real
// secrets file still call t.Setenv, which overrides this and restores it.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunMain(m))
}
