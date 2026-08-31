package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Resolve loads the project's cloudlab.pkl at path, merges it with a
// personal base config if one is found (see resolveBasePath), and
// returns the merged Config. Scalar fields take the project's value if
// set, else the base's; list fields are additive (base's entries
// first, then the project's). Returns an error naming any of
// region/size/template still unset after merging.
//
// Named Resolve, not Load, because the generated Config.pkl.go already
// defines a lower-level Load(ctx, evaluator, source) — this function
// builds on LoadFromPath (also generated) instead.
func Resolve(ctx context.Context, path string) (Config, error) {
	project, err := LoadFromPath(ctx, path)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Config{}, fmt.Errorf("pkl CLI not found on PATH (run inside `nix develop`, or install it: https://pkl-lang.org/main/current/pkl-cli/index.html#installation): %w", err)
		}
		return Config{}, fmt.Errorf("loading %s: %w", path, err)
	}

	basePath, err := resolveBasePath(path, project.BasePath)
	if err != nil {
		return Config{}, fmt.Errorf("resolving base config path: %w", err)
	}

	merged := project
	if _, statErr := os.Stat(basePath); statErr == nil {
		base, err := LoadFromPath(ctx, basePath)
		if err != nil {
			return Config{}, fmt.Errorf("loading base config %s: %w", basePath, err)
		}
		merged = mergeConfig(base, project)
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("checking base config %s: %w", basePath, statErr)
	}

	if err := validate(merged); err != nil {
		return Config{}, err
	}
	return merged, nil
}

// mergeConfig layers project over base: scalars use project's value if
// set, else base's; lists are additive, base's entries first.
//
// BasePath is intentionally not carried into the returned Config — it's
// only meaningful for deciding which base file to merge, not as part of
// the merged output.
func mergeConfig(base, project Config) Config {
	return Config{
		Region:   coalesce(project.Region, base.Region),
		Size:     coalesce(project.Size, base.Size),
		Template: coalesce(project.Template, base.Template),
		SshKeys:  mergeStringSlicePtrs(base.SshKeys, project.SshKeys),
		Packages: append(append([]string{}, base.Packages...), project.Packages...),
		Flakes:   append(append([]Flake{}, base.Flakes...), project.Flakes...),
	}
}

func coalesce(project, base *string) *string {
	if project != nil {
		return project
	}
	return base
}

func mergeStringSlicePtrs(base, project *[]string) *[]string {
	if base == nil && project == nil {
		return nil
	}
	merged := []string{}
	if base != nil {
		merged = append(merged, *base...)
	}
	if project != nil {
		merged = append(merged, *project...)
	}
	return &merged
}

func validate(c Config) error {
	var missing []string
	if c.Region == nil {
		missing = append(missing, "region")
	}
	if c.Size == nil {
		missing = append(missing, "size")
	}
	if c.Template == nil {
		missing = append(missing, "template")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s) after merging project and base config: %s", strings.Join(missing, ", "))
	}
	return nil
}
