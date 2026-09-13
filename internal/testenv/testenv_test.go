package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/xdg"
)

// The property the whole package exists for: inside Run, every path cloudlab
// derives from the user's home lands in the sandbox.
func TestRun_EveryHomeDerivedPathLandsInTheSandbox(t *testing.T) {
	realHome, _ := os.UserHomeDir()

	Run(t, func(t *testing.T, env Env) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("UserHomeDir() error = %v", err)
		}
		if home != env.Home {
			t.Errorf("UserHomeDir() = %q, want the sandbox %q", home, env.Home)
		}
		if realHome != "" && strings.HasPrefix(home, realHome) && home != realHome {
			t.Errorf("sandbox %q sits inside the real home %q", home, realHome)
		}

		// Config and State hold values a test can observe, so they must be
		// inside the sandbox.
		for _, base := range []xdg.Base{xdg.Config, xdg.State} {
			got, err := xdg.Path(base, "thing")
			if err != nil {
				t.Fatalf("xdg.Path() error = %v", err)
			}
			if !strings.HasPrefix(got, env.Home) {
				t.Errorf("xdg.Path(%d) = %q, want it under the sandbox %q", base, got, env.Home)
			}
		}
	})
}

// Cache is deliberately NOT sandboxed: it holds only regenerable data, and
// isolating it sends Pkl to a cold package cache on every run for no gain.
// Pinned because it is a conscious exception, and an exception nobody wrote
// down is indistinguishable from a hole.
func TestRun_LeavesTheSharedCacheAlone(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", shared)

	Run(t, func(t *testing.T, env Env) {
		got, err := xdg.Path(xdg.Cache, "pkl")
		if err != nil {
			t.Fatalf("xdg.Path() error = %v", err)
		}
		if !strings.HasPrefix(got, shared) {
			t.Errorf("cache path = %q, want it still under the shared cache %q", got, shared)
		}
	})
}

// The sandbox lasts for the whole test, not just the lambda.
//
// t.Setenv restores at the end of the test that called it, and Run is called
// with the enclosing test's t -- so HOME is still redirected after Run
// returns. That is the honest contract and worth pinning, because the
// tempting assumption is the opposite: code after Run in the same test
// function is still sandboxed, and a second Run in one test nests rather
// than resets.
func TestRun_SandboxOutlivesTheLambda(t *testing.T) {
	var sandbox string
	Run(t, func(t *testing.T, env Env) { sandbox = env.Home })

	if got := os.Getenv("HOME"); got != sandbox {
		t.Errorf("HOME = %q after Run returned, want it still pointing at the sandbox %q", got, sandbox)
	}
}

// Restoration happens, just at the boundary the testing package owns: a
// subtest's sandbox is gone once that subtest ends.
func TestRun_SandboxIsGoneAfterTheSubtestThatOwnedIt(t *testing.T) {
	before := os.Getenv("HOME")
	var sandbox string
	t.Run("owns a sandbox", func(t *testing.T) {
		Run(t, func(t *testing.T, env Env) { sandbox = env.Home })
	})

	if sandbox == before {
		t.Fatal("the subtest did not get its own sandbox")
	}
	if got := os.Getenv("HOME"); got != before {
		t.Errorf("HOME = %q, want the subtest's sandbox rolled back to %q", got, before)
	}
}

func TestEnvPath_JoinsOntoTheSandbox(t *testing.T) {
	Run(t, func(t *testing.T, env Env) {
		if got, want := env.Path(".ssh", "known_hosts"), filepath.Join(env.Home, ".ssh", "known_hosts"); got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}
