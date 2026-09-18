package config

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jskswamy/bivouac/internal/testenv"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func TestExamples_Minimal_LoadsCleanly(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "examples", "minimal", "bivouac.pkl")

	// Isolate from any real user's machine-local base config — this
	// example must load cleanly on its own, not merged with whatever
	// happens to live at $XDG_CONFIG_HOME/bivouac/base.pkl.
	testenv.Isolate(t)

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", path, err)
	}
	if cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
	if cfg.Template != "python" {
		t.Errorf("Template = %v, want python", cfg.Template)
	}
}

func TestExamples_WithBase_MergesCleanly(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "examples", "with-base", "bivouac.pkl")

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", path, err)
	}
	if cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3 (from project)", cfg.Region)
	}
	if cfg.Size != "s-1vcpu-1gb" {
		t.Errorf("Size = %v, want s-1vcpu-1gb (from base)", cfg.Size)
	}
	wantPackages := []string{"git", "ripgrep", "jq"}
	if !equalStrings(cfg.Packages, wantPackages) {
		t.Errorf("Packages = %v, want %v (base then project)", cfg.Packages, wantPackages)
	}
}
