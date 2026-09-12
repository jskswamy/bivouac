package xdg

import (
	"path/filepath"
	"testing"
)

// Both branches of all three bases. The table is exhaustive over Base on
// purpose: a base added later has no env var and no fallback until someone
// fills them in, and this is where that shows up.
func TestPath_EnvSetAndFallback(t *testing.T) {
	tests := []struct {
		name     string
		base     Base
		env      string
		fallback []string
	}{
		{"config", Config, "XDG_CONFIG_HOME", []string{".config"}},
		{"cache", Cache, "XDG_CACHE_HOME", []string{".cache"}},
		{"state", State, "XDG_STATE_HOME", []string{".local", "state"}},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/env set", func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(tt.env, dir)

			got, err := Path(tt.base, "thing.json")
			if err != nil {
				t.Fatalf("Path() error = %v", err)
			}
			if want := filepath.Join(dir, "cloudlab", "thing.json"); got != want {
				t.Errorf("Path() = %q, want %q", got, want)
			}
		})

		t.Run(tt.name+"/empty env falls back", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			// Empty rather than unset: that is the state cloudlab's own
			// tests put these variables in to reach the fallback, so it has
			// to mean "absent" and not "the empty path".
			t.Setenv(tt.env, "")

			got, err := Path(tt.base, "thing.json")
			if err != nil {
				t.Fatalf("Path() error = %v", err)
			}
			want := filepath.Join(append(append([]string{home}, tt.fallback...), "cloudlab", "thing.json")...)
			if got != want {
				t.Errorf("Path() = %q, want %q", got, want)
			}
		})
	}
}

// The bases must not collide -- a shared "cloudlab" segment under three
// different roots is the whole point, and a copy-paste in env() or
// fallback() would silently alias two of them.
func TestPath_BasesResolveToDifferentRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, env := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(env, "")
	}

	seen := map[string]Base{}
	for _, b := range []Base{Config, Cache, State} {
		got, err := Path(b, "state.json")
		if err != nil {
			t.Fatalf("Path(%d) error = %v", b, err)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("Base %d and %d both resolve to %q", other, b, got)
		}
		seen[got] = b
	}
}

// Callers pass a leaf name, not a directory: nothing outside this package
// should ever have to know the "cloudlab" segment exists.
func TestPath_JoinsSeveralPartsUnderCloudlab(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	got, err := Path(Cache, "pkl")
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if want := filepath.Join(dir, "cloudlab", "pkl"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}
