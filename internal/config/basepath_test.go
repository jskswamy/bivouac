package config

import (
	"path/filepath"
	"testing"
)

func TestResolveBasePath_NoOverride_UsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")

	got, err := resolveBasePath("/repo/cloudlab.pkl", nil)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/xdg/config", "cloudlab", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_NoOverride_NoXDG_UsesHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/alice")

	got, err := resolveBasePath("/repo/cloudlab.pkl", nil)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/home/alice", ".config", "cloudlab", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_OverrideAbsolute_UsedAsIs(t *testing.T) {
	override := "/custom/base.pkl"

	got, err := resolveBasePath("/repo/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	if got != override {
		t.Errorf("got %q, want %q", got, override)
	}
}

func TestResolveBasePath_OverrideHomeRelative_Expands(t *testing.T) {
	t.Setenv("HOME", "/home/alice")
	override := "~/work-base.pkl"

	got, err := resolveBasePath("/repo/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/home/alice", "work-base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_OverrideRelative_ResolvesAgainstProjectDir(t *testing.T) {
	override := "./base.pkl"

	got, err := resolveBasePath("/repo/nested/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/repo/nested", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBasePath_OverrideEmptyString_TreatedAsUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	override := ""

	got, err := resolveBasePath("/repo/cloudlab.pkl", &override)
	if err != nil {
		t.Fatalf("resolveBasePath() error = %v", err)
	}

	want := filepath.Join("/xdg/config", "cloudlab", "base.pkl")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
