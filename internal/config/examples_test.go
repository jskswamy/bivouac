package config

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
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
	path := filepath.Join(repoRoot(t), "docs", "examples", "minimal", "cloudlab.pkl")

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", path, err)
	}
	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
	if cfg.Template == nil || *cfg.Template != "python" {
		t.Errorf("Template = %v, want python", cfg.Template)
	}
}

func TestExamples_WithBase_MergesCleanly(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "examples", "with-base", "cloudlab.pkl")

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve(%s) error = %v", path, err)
	}
	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3 (from project)", cfg.Region)
	}
	if cfg.Size == nil || *cfg.Size != "s-1vcpu-1gb" {
		t.Errorf("Size = %v, want s-1vcpu-1gb (from base)", cfg.Size)
	}
	wantPackages := []string{"git", "ripgrep", "jq"}
	if !equalStrings(cfg.Packages, wantPackages) {
		t.Errorf("Packages = %v, want %v (base then project)", cfg.Packages, wantPackages)
	}
}
