package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// schemaPath returns the absolute path to pkl/Config.pkl regardless of
// the test binary's working directory.
func schemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "pkl", "Config.pkl")
}

func TestLoadFromPath_MinimalFixture(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "cloudlab.pkl")
	content := "amends " + quotePklString(schemaPath(t)) + "\n\n" +
		"region = \"nyc3\"\n" +
		"packages {\n  \"ripgrep\"\n}\n"
	if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	cfg, err := LoadFromPath(context.Background(), fixture)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.Region == nil || *cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want \"nyc3\"", cfg.Region)
	}
	if cfg.Size != nil {
		t.Errorf("Size = %v, want nil", cfg.Size)
	}
	if len(cfg.Packages) != 1 || cfg.Packages[0] != "ripgrep" {
		t.Errorf("Packages = %v, want [ripgrep]", cfg.Packages)
	}
	if cfg.Flakes == nil || len(cfg.Flakes) != 0 {
		t.Errorf("Flakes = %v, want empty non-nil slice", cfg.Flakes)
	}
}

// quotePklString renders s as a Pkl double-quoted string literal. Test
// fixture paths are plain filesystem paths (no quotes/backslashes to
// escape), so a bare wrap is sufficient here.
func quotePklString(s string) string {
	return "\"" + s + "\""
}
