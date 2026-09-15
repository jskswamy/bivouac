package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Each file's paths resolve against its own directory, and base's come
// first. A merged Config cannot express this: pkl concatenates the two
// listings and nothing records which file contributed which entry.
func TestInstructionFiles_ResolvesAgainstTheDeclaringFile(t *testing.T) {
	home := t.TempDir()
	baseDir := filepath.Join(home, "config")
	projectDir := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(baseDir, "instructions"))
	mustMkdirAll(t, filepath.Join(projectDir, "docs"))

	mustWrite(t, filepath.Join(baseDir, "instructions", "mine.md"), "personal\n")
	mustWrite(t, filepath.Join(baseDir, "instructions", "second.md"), "personal too\n")
	mustWrite(t, filepath.Join(projectDir, "docs", "theirs.md"), "project\n")

	basePath := filepath.Join(baseDir, "base.pkl")
	mustWrite(t, basePath, renderFor(t, Values{
		// Two entries here, in this order, so the assertion below also
		// pins that declaration order survives within a single file, not
		// only across base and project.
		FieldInstructions: []string{"instructions/mine.md", "instructions/second.md"},
	}))
	projectPath := filepath.Join(projectDir, "cloudlab.pkl")
	mustWrite(t, projectPath, renderFor(t, Values{
		FieldRegion:       "nyc3",
		FieldSize:         "s-1vcpu-1gb",
		FieldTemplate:     "docker",
		FieldBasePath:     basePath,
		FieldInstructions: []string{"docs/theirs.md"},
	}))

	got, err := InstructionFiles(context.Background(), projectPath)
	if err != nil {
		t.Fatalf("InstructionFiles() error = %v", err)
	}
	want := []string{
		filepath.Join(baseDir, "instructions", "mine.md"),
		filepath.Join(baseDir, "instructions", "second.md"),
		filepath.Join(projectDir, "docs", "theirs.md"),
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("InstructionFiles() = %#v, want %#v", got, want)
	}
}

// A silently absent workflow is the failure this design exists to
// prevent, so a missing file stops the flow and names the path.
func TestInstructionFiles_MissingFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "cloudlab.pkl")
	mustWrite(t, projectPath, renderFor(t, Values{
		FieldRegion:       "nyc3",
		FieldSize:         "s-1vcpu-1gb",
		FieldTemplate:     "docker",
		FieldInstructions: []string{"docs/gone.md"},
	}))

	_, err := InstructionFiles(context.Background(), projectPath)
	if err == nil {
		t.Fatal("InstructionFiles() error = nil, want an error naming the path")
	}
	if !strings.Contains(err.Error(), "docs/gone.md") {
		t.Errorf("error %q does not name the missing path", err)
	}
}

// A base.pkl that exists but fails to parse must not be mistaken for a
// project with no personal base config at all: the two are very
// different situations, and conflating them would hide a broken base
// behind an empty instructions list instead of reporting it.
func TestInstructionFiles_MalformedBaseIsAnError(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "repo")
	mustMkdirAll(t, projectDir)

	basePath := filepath.Join(home, "config", "base.pkl")
	mustMkdirAll(t, filepath.Dir(basePath))
	mustWrite(t, basePath, "this is not valid pkl {{{\n")

	projectPath := filepath.Join(projectDir, "cloudlab.pkl")
	mustWrite(t, projectPath, renderFor(t, Values{
		FieldRegion:   "nyc3",
		FieldSize:     "s-1vcpu-1gb",
		FieldTemplate: "docker",
		FieldBasePath: basePath,
	}))

	_, err := InstructionFiles(context.Background(), projectPath)
	if err == nil {
		t.Fatal("InstructionFiles() error = nil, want an error for the malformed base config")
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func renderFor(t *testing.T, v Values) string {
	t.Helper()
	s, err := Render(v)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return s
}
