package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolved_MinimalFixture(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "bivouac.pkl")
	content := "region = \"nyc3\"\n" +
		"packages {\n  \"ripgrep\"\n}\n"
	if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	cfg, err := loadResolved(context.Background(), fixture)
	if err != nil {
		t.Fatalf("loadResolved() error = %v", err)
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
