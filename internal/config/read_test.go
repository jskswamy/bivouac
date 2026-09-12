package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDeclaredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "plain assignments and listings",
			body: "region = \"nyc3\"\npackages {\n  \"jq\"\n}\n",
			want: []string{"packages", "region"},
		},
		{
			// The whole reason this is not a grep: a Flake's own
			// `packages` sits two levels in, and counting it would make
			// every file with a flake look like it pins packages.
			name: "nested flake properties are not top-level fields",
			body: "flakes {\n  new Flake {\n    url = \"github:foo/bar\"\n    packages {\n      \"default\"\n    }\n    modules = true\n  }\n}\n",
			want: []string{"flakes"},
		},
		{
			name: "line comments are not declarations",
			body: "// size = \"s-1vcpu-1gb\"\nregion = \"nyc3\"  // size comes from base\n",
			want: []string{"region"},
		},
		{
			name: "block comments are not declarations",
			body: "/*\nsize = \"s-1vcpu-1gb\"\ntemplate = \"python\"\n*/\nregion = \"nyc3\"\n",
			want: []string{"region"},
		},
		{
			// Braces inside a value must not shift the nesting level,
			// or every field after one would be invisible.
			name: "braces inside strings do not open a block",
			body: "region = \"ny{c3\"\nsize = \"s-1vcpu-1gb\"\n",
			want: []string{"region", "size"},
		},
		{
			name: "escaped quote does not end the string",
			body: "region = \"a\\\"{b\"\nsize = \"s-1vcpu-1gb\"\n",
			want: []string{"region", "size"},
		},
		{
			name: "fields outside the schema are ignored",
			body: "notAField = \"x\"\nregion = \"nyc3\"\n",
			want: []string{"region"},
		},
		{
			name: "empty file declares nothing",
			body: "\n// nothing here\n",
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := declaredFields([]byte(tt.body))
			names := make([]string, 0, len(got))
			for name := range got {
				names = append(names, name)
			}
			sort.Strings(names)
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("declaredFields() = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestReadValues_OnlyWhatTheFileDeclares(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, path, strings.Join([]string{
		`region = "nyc3"`,
		`template = "python"`,
		`beads = "dolthub"`,
		`packages {`,
		`  "jq"`,
		`}`,
	}, "\n")+"\n")

	got, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadValues() error = %v", err)
	}

	want := Values{
		"region":   "nyc3",
		"template": "python",
		"beads":    "dolthub",
		"packages": []string{"jq"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadValues() = %#v, want %#v", got, want)
	}
}

// A file that says nothing must not come back carrying the schema's
// defaults: writing those back is the whole-file diff the design rejects.
func TestReadValues_DefaultsAreNotInvented(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base.pkl")
	writeFixture(t, path, "size = \"s-1vcpu-1gb\"\n")

	got, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadValues() error = %v", err)
	}
	if !reflect.DeepEqual(got, Values{"size": "s-1vcpu-1gb"}) {
		t.Errorf("ReadValues() = %#v, want just size", got)
	}
}

// ReadValues must not apply Resolve's required-field check: presets and
// base.pkl are partial by definition.
func TestReadValues_PartialFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preset.pkl")
	writeFixture(t, path, "template = \"python\"\nagents {\n  \"claude\"\n}\n")

	got, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadValues() error = %v", err)
	}
	if !reflect.DeepEqual(got, Values{"template": "python", "agents": []string{"claude"}}) {
		t.Errorf("ReadValues() = %#v", got)
	}
}

// The round trip the design asks for: what Render writes, ReadValues
// reads back unchanged.
func TestReadValues_RoundTripsRender(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-such-config"))

	want := Values{
		"region":    "nyc3",
		"size":      "s-2vcpu-4gb",
		"template":  "python",
		"arch":      "arm64",
		"tailscale": true,
		"beads":     "off",
		"sshKeys":   []string{"AAAA...fingerprint"},
		"packages":  []string{"ripgrep", "jq"},
		"agents":    []string{"claude"},
		"flakes":    []Flake{{Url: "github:foo/bar", Packages: []string{"default"}, Modules: true}},
	}

	rendered, err := Render(want)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	path := filepath.Join(dir, "cloudlab.pkl")
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatalf("writing rendered config: %v", err)
	}

	got, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadValues() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadValues(Render(v)) = %#v, want %#v", got, want)
	}
}

// The repository's own examples are the realistic input: whatever they
// declare must survive a read and a re-render.
func TestReadValues_ExampleReRendersToTheSameFields(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("docs", "examples", "minimal", "cloudlab.pkl"),
		filepath.Join("docs", "examples", "with-base", "base.pkl"),
		filepath.Join("docs", "examples", "with-base", "cloudlab.pkl"),
	} {
		t.Run(rel, func(t *testing.T) {
			path := filepath.Join(repoRoot(t), rel)
			values, err := ReadValues(context.Background(), path)
			if err != nil {
				t.Fatalf("ReadValues(%s) error = %v", rel, err)
			}
			rendered, err := Render(values)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			reread := declaredFields([]byte(rendered))
			original := declaredFields(mustRead(t, path))
			if !reflect.DeepEqual(reread, original) {
				t.Errorf("re-rendered fields = %v, want %v", reread, original)
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return raw
}
