package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points XDG_CONFIG_HOME at an empty directory for the whole
// package before any test runs.
//
// resolveToken falls back to the personal secrets file whenever
// DIGITALOCEAN_TOKEN is empty, so without this a test that clears the
// token -- or simply runs somewhere it was never exported -- reads the
// developer's own ~/.config/cloudlab/secrets.yaml. With a
// YubiKey-backed age identity that does not fail, it blocks: `go test
// ./cmd/` sits waiting for a touch with no output explaining why.
//
// Done here rather than per test on purpose. Relying on each test to
// remember is the kind of ambient-config leak that made
// TestVerifySignatures_RejectsUnsignedCommits fail on the maintainer's
// machine and pass everywhere else (cloudlab-9pk); a package-wide floor
// cannot be forgotten by the next test added. Tests that want a real
// secrets file still call t.Setenv, which overrides this and restores it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cloudlab-cmd-config")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty")); err != nil {
		panic(err)
	}

	// os.Exit skips deferred functions, so cleanup runs explicitly
	// between m.Run and the exit rather than via defer.
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
