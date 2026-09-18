package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRender_FieldsFollowSchemaOrder(t *testing.T) {
	// Deliberately built in an order unrelated to the schema's: the
	// renderer's job is to impose Config.pkl's order, not the caller's.
	got, err := Render(Values{
		"packages":  []string{"ripgrep", "jq"},
		"template":  "python",
		"region":    "nyc3",
		"tailscale": true,
		"size":      "s-1vcpu-1gb",
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`tailscale = true`,
		`packages {`,
		`  "ripgrep"`,
		`  "jq"`,
		`}`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("Render() =\n%s\nwant\n%s", got, want)
	}
}

func TestRender_EmptyListingStaysOnOneLine(t *testing.T) {
	got, err := Render(Values{"agents": []string{}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != "agents {}\n" {
		t.Errorf("Render() = %q, want %q", got, "agents {}\n")
	}
}

func TestRender_NoFieldsIsEmpty(t *testing.T) {
	got, err := Render(Values{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != "" {
		t.Errorf("Render() = %q, want empty", got)
	}
}

func TestRender_Flakes(t *testing.T) {
	got, err := Render(Values{"flakes": []Flake{
		{Url: "github:nix-community/fenix", Packages: []string{"default"}, Modules: []string{"tools.git"}},
		{Url: "github:foo/bar", Packages: []string{}},
	}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := strings.Join([]string{
		`flakes {`,
		`  new Flake {`,
		`    url = "github:nix-community/fenix"`,
		`    packages {`,
		`      "default"`,
		`    }`,
		`    modules {`,
		`      "tools.git"`,
		`    }`,
		`  }`,
		`  new Flake {`,
		`    url = "github:foo/bar"`,
		`    packages {}`,
		`    modules {}`,
		`  }`,
		`}`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("Render() =\n%s\nwant\n%s", got, want)
	}
}

func TestRender_EscapesStrings(t *testing.T) {
	// `\(` opens interpolation in pkl, so a lone backslash in a value
	// would otherwise turn the rest of the string into an expression.
	got, err := Render(Values{"region": `a"b\c`})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != `region = "a\"b\\c"`+"\n" {
		t.Errorf("Render() = %q", got)
	}
}

func TestRender_RejectsUnknownField(t *testing.T) {
	if _, err := Render(Values{"nope": "x"}); err == nil {
		t.Fatal("Render() error = nil, want an error naming the unknown field")
	}
}

func TestRender_RejectsWrongType(t *testing.T) {
	if _, err := Render(Values{"region": 42}); err == nil {
		t.Fatal("Render() error = nil, want an error for a non-string region")
	}
	if _, err := Render(Values{"tailscale": "yes"}); err == nil {
		t.Fatal("Render() error = nil, want an error for a non-bool tailscale")
	}
}

// The round trip the spec requires: anything Render emits must come back
// through Resolve. A wizard that writes pkl which will not parse is worse
// than no wizard, and this is the only check that proves it did not.
func TestRender_RoundTripsThroughResolve(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	values := Values{
		"region":    "nyc3",
		"size":      "s-2vcpu-4gb",
		"template":  "python",
		"arch":      "arm64",
		"image":     "ubuntu-24-04-x64",
		"tailscale": true,
		"beads":     "dolthub",
		"sshKeys":   []string{"AAAA...fingerprint"},
		"packages":  []string{"ripgrep", "jq"},
		"agents":    []string{"claude", "codex"},
		"flakes":    []Flake{{Url: "github:foo/bar", Packages: []string{"default"}, Modules: []string{"default"}}},
	}

	rendered, err := Render(values)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	path := filepath.Join(dir, "bivouac.pkl")
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatalf("writing rendered config: %v", err)
	}

	cfg, err := Resolve(context.Background(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v\nrendered:\n%s", err, rendered)
	}
	if cfg.Region != "nyc3" {
		t.Errorf("Region = %v, want nyc3", cfg.Region)
	}
	if cfg.Arch != "arm64" {
		t.Errorf("Arch = %q, want arm64", cfg.Arch)
	}
	if cfg.Beads != "dolthub" {
		t.Errorf("Beads = %q, want dolthub", cfg.Beads)
	}
	if !equalStrings(cfg.Agents, []string{"claude", "codex"}) {
		t.Errorf("Agents = %v, want [claude codex]", cfg.Agents)
	}
	if len(cfg.Flakes) != 1 || cfg.Flakes[0].Url != "github:foo/bar" || len(cfg.Flakes[0].Modules) != 1 || cfg.Flakes[0].Modules[0] != "default" {
		t.Errorf("Flakes = %+v", cfg.Flakes)
	}
}

// A config declaring instructions must survive ReadValues then Render
// unchanged. declaredFields filters through KnownField, so a field added
// to Config.pkl but not to fieldKinds is invisible here and `bivouac
// init` would drop the line when it rewrites the file.
func TestReadValues_KeepsInstructions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bivouac.pkl")
	rendered, err := Render(Values{
		FieldRegion:       "nyc3",
		FieldSize:         "s-1vcpu-1gb",
		FieldTemplate:     "docker",
		FieldInstructions: []string{"docs/workflow.md", "docs/extra.md"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadValues() error = %v\nrendered:\n%s", err, rendered)
	}
	want := []string{"docs/workflow.md", "docs/extra.md"}
	if !reflect.DeepEqual(want, got[FieldInstructions]) {
		t.Errorf("instructions round trip = %#v, want %#v", got[FieldInstructions], want)
	}
}

func TestRender_Settings(t *testing.T) {
	got, err := Render(Values{"settings": map[string]any{
		"programs.git.userEmail": "work@example.com",
		"tools.tig.enable":       false,
		"retries":                3,
		"tags":                   []string{"a", "b"},
	}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	// Map iteration order is not guaranteed, so check for each line's
	// presence rather than the whole block's exact text.
	for _, want := range []string{
		`settings {`,
		`  ["programs.git.userEmail"] = "work@example.com"`,
		`  ["tools.tig.enable"] = false`,
		`  ["retries"] = 3`,
		`  ["tags"] = new Listing { "a"; "b" }`,
		`}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing line %q in:\n%s", want, got)
		}
	}
}

func TestRender_EmptySettings(t *testing.T) {
	got, err := Render(Values{"settings": map[string]any{}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(got, "settings {}\n") {
		t.Errorf("Render() = %q, want an empty settings block", got)
	}
}
