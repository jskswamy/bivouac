package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/provider"
)

func TestHerdrTabsFor_NoConfigMeansNoLayout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	if got := herdrTabsFor(ctx, t.TempDir()); len(got) != 0 {
		t.Errorf("herdrTabsFor() = %+v, want none for a repository without bivouac.pkl", got)
	}
	if got := herdrTabsFor(ctx, ""); len(got) != 0 {
		t.Errorf("herdrTabsFor(\"\") = %+v, want none", got)
	}
}

func TestHerdrTabsFor_ReadsTheRepositorysTabs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	repo := t.TempDir()
	content := "region = \"nyc3\"\nsize = \"s\"\ntemplate = \"python\"\n" +
		"herdrTabs {\n  new HerdrTab {\n    label = \"run\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "bivouac.pkl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := herdrTabsFor(ctx, repo)
	if len(got) != 1 || got[0].Label != "run" {
		t.Errorf("herdrTabsFor() = %+v, want the run tab", got)
	}
}

// A config that will not resolve costs the layout, not the attach.
func TestHerdrTabsFor_BrokenConfigDegradesToNoLayout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "bivouac.pkl"), []byte("herdrTabs = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := herdrTabsFor(ctx, repo); len(got) != 0 {
		t.Errorf("herdrTabsFor() = %+v, want none", got)
	}
	if !strings.Contains(errOut.String(), "bivouac.pkl") {
		t.Errorf("errOut = %q, want a warning naming bivouac.pkl so the degrade is visible", errOut.String())
	}
}
