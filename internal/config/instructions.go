package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// InstructionFiles returns the absolute path of every Markdown file the
// config chain declares, base's before the project's, each in its own
// declaration order.
//
// Read per file rather than off a merged Config, because merging is what
// destroys the information this needs. pkl concatenates the two listings
// into one, and a relative path in the result cannot say whether it meant
// ~/.config/bivouac or the repository root.
//
// A missing file is an error rather than a warning. A workflow that
// silently fails to arrive is the failure the whole feature exists to
// prevent, and unlike an absent optional credential there is no degraded
// mode worth continuing into.
func InstructionFiles(ctx context.Context, projectPath string) ([]string, error) {
	project, err := ReadValues(ctx, projectPath)
	if err != nil {
		return nil, err
	}

	var override *string
	if s, ok := project[FieldBasePath].(string); ok {
		override = &s
	}

	var files []string
	basePath, err := resolveBasePath(projectPath, override)
	// Stat first, mirroring Resolve (config.go): a base that does not
	// exist is ordinary, not every project has a personal base config,
	// but one that exists and fails to load is a real error and must be
	// reported as one rather than treated the same as "absent".
	if err == nil {
		if _, statErr := os.Stat(basePath); statErr == nil {
			base, berr := ReadValues(ctx, basePath)
			if berr != nil {
				return nil, berr
			}
			resolved, rerr := resolveAgainst(filepath.Dir(basePath), base)
			if rerr != nil {
				return nil, rerr
			}
			files = append(files, resolved...)
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("checking base config %s: %w", basePath, statErr)
		}
	}

	resolved, err := resolveAgainst(filepath.Dir(projectPath), project)
	if err != nil {
		return nil, err
	}
	return append(files, resolved...), nil
}

// resolveAgainst turns one file's declared instructions into absolute
// paths rooted at dir, failing on the first that does not exist.
func resolveAgainst(dir string, v Values) ([]string, error) {
	declared, ok := v[FieldInstructions].([]string)
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(declared))
	for _, rel := range declared {
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, rel)
		}
		// #nosec G304 -- path is built from a declared instructions entry
		// under the config file's own directory, same rationale as
		// ReadValues above.
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("instructions: %s: %w", rel, err)
		}
		out = append(out, path)
	}
	return out, nil
}
