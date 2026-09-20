package cmd

import (
	"bytes"
	"context"
	"io"
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

// Tabs declared once in base.pkl are meant to reach every instance. A
// repository with no bivouac.pkl used to lose them, because Resolve is
// anchored on the project file.
func TestHerdrTabsFor_ReadsTheBasesTabsWithoutAProjectConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	if err := os.MkdirAll(filepath.Join(cfgDir, "bivouac"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := "herdrTabs {\n  new HerdrTab {\n    label = \"agent\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "bivouac", "base.pkl"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	got := herdrTabsFor(ctx, t.TempDir())

	if len(got) != 1 || got[0].Label != "agent" {
		t.Errorf("herdrTabsFor() = %+v, want the base's agent tab", got)
	}
}

// No repository at all is the same question with the same answer: herdr
// can be run against an instance with no session resolved.
func TestHerdrTabsFor_ReadsTheBasesTabsWithNoRepositoryAtAll(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	if err := os.MkdirAll(filepath.Join(cfgDir, "bivouac"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := "herdrTabs {\n  new HerdrTab {\n    label = \"agent\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "bivouac", "base.pkl"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	got := herdrTabsFor(ctx, "")

	if len(got) != 1 || got[0].Label != "agent" {
		t.Errorf("herdrTabsFor(\"\") = %+v, want the base's agent tab", got)
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

// The prompt is the only thing between `pair` and a QR code, so without a
// terminal it has to resolve itself. Reading stdin there blocks forever on
// input that will never arrive, which is the bug this guards.
func TestAskPairHost_TakesTheTailnetAddressWithoutATerminal(t *testing.T) {
	c, out := bufferedCmd(t)
	// Open, and readable, and not a terminal: the shape a script or an
	// agent hands the command. A read here would block, not fail.
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	c.SetIn(pr)

	got, err := askPairHost(c, "100.64.0.1", "139.59.12.44", false)

	if err != nil {
		t.Fatalf("askPairHost() error = %v, want nil", err)
	}
	if got != "100.64.0.1" {
		t.Errorf("askPairHost() = %q, want the tailnet address taken as the default", got)
	}
	if !strings.Contains(out.String(), "100.64.0.1") {
		t.Errorf("out = %q, want the address it chose named", out.String())
	}
	if strings.Contains(out.String(), "Choose [1]") {
		t.Errorf("out = %q, want no prompt with no terminal to answer it", out.String())
	}
}

func TestAskPairHost_AsksWithATerminal(t *testing.T) {
	for _, tc := range []struct{ answer, want string }{
		{"2\n", "139.59.12.44"},
		{"1\n", "100.64.0.1"},
		{"\n", "100.64.0.1"},
		{"", "100.64.0.1"}, // EOF: the default, not an error
	} {
		c, out := bufferedCmd(t)
		c.SetIn(strings.NewReader(tc.answer))

		got, err := askPairHost(c, "100.64.0.1", "139.59.12.44", true)

		if err != nil {
			t.Fatalf("askPairHost(%q) error = %v, want nil", tc.answer, err)
		}
		if got != tc.want {
			t.Errorf("askPairHost(%q) = %q, want %q", tc.answer, got, tc.want)
		}
		if !strings.Contains(out.String(), "Choose [1]") {
			t.Errorf("out = %q, want the prompt", out.String())
		}
	}
}
