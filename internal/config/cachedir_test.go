package config

import (
	"path/filepath"
	"testing"
)

func TestPklCacheDir_UsesXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")

	got, err := pklCacheDir()
	if err != nil {
		t.Fatalf("pklCacheDir() error = %v", err)
	}

	want := filepath.Join("/xdg/cache", "cloudlab", "pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPklCacheDir_NoXDG_FallsBackToHomeCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/alice")

	got, err := pklCacheDir()
	if err != nil {
		t.Fatalf("pklCacheDir() error = %v", err)
	}

	want := filepath.Join("/home/alice", ".cache", "cloudlab", "pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
