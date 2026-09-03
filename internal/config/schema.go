package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed Config.pkl
var embeddedSchema []byte

// injectSchema copies raw (a project or personal-base file's content,
// which must not declare its own amends) into a fresh temp file that
// amends cloudlab's own embedded schema, so callers never need to know
// or reference the schema's location themselves. Returns the temp
// file's path and a cleanup func that removes its containing directory
// (including the embedded schema copy written alongside it).
func injectSchema(path string, raw []byte) (tmpPath string, cleanup func(), err error) {
	if hasAmends(raw) {
		return "", nil, fmt.Errorf("%s: must not declare its own `amends` — cloudlab manages the schema reference automatically; remove that line", path)
	}

	dir, err := os.MkdirTemp("", "cloudlab-config-*")
	if err != nil {
		return "", nil, fmt.Errorf("preparing schema for %s: %w", path, err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	schemaPath := filepath.Join(dir, "Config.pkl")
	if err := os.WriteFile(schemaPath, embeddedSchema, 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("preparing schema for %s: %w", path, err)
	}

	// tmpPath is dir (our own fresh os.MkdirTemp) joined with
	// filepath.Base(path), which strips any directory components from
	// path, so no traversal outside dir is possible regardless of what
	// path contains.
	tmpPath = filepath.Join(dir, filepath.Base(path))
	content := "amends " + quotePklString(schemaPath) + "\n\n" + string(raw)
	// #nosec G703 -- see tmpPath's construction above.
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("preparing %s: %w", path, err)
	}
	return tmpPath, cleanup, nil
}

// hasAmends reports whether raw's content already starts with its own
// amends declaration.
func hasAmends(raw []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(raw), []byte("amends"))
}

// quotePklString renders s as a Pkl double-quoted string literal.
// Schema and fixture paths are plain filesystem paths with no
// quotes/backslashes to escape, so a bare wrap is sufficient here.
func quotePklString(s string) string {
	return `"` + s + `"`
}
