package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/apple/pkl-go/pkl"

	"github.com/jskswamy/bivouac/internal/tool"
)

// Resolve loads the project's bivouac.pkl at path, merges it with a
// personal base config if one is found (see resolveBasePath), and
// returns the merged Config. Scalar fields take the project's value if
// set, else the base's; list fields are additive (base's entries
// first, then the project's). Returns an error naming any of
// region/size/template still unset after merging.
//
// Named Resolve, not Load, because the generated Config.pkl.go already
// defines a lower-level Load(ctx, evaluator, source) — this function
// builds on that instead of the generated LoadFromPath, so it can point
// the evaluator's package cache at pklCacheDir() rather than pkl-go's
// hardcoded ~/.pkl/cache default (LoadFromPath exposes no override hook
// for this).
func Resolve(ctx context.Context, path string) (Resolved, error) {
	if _, err := tool.Require("pkl"); err != nil {
		return Resolved{}, err
	}

	project, err := loadResolved(ctx, path)
	if err != nil {
		return Resolved{}, err
	}

	basePath, err := resolveBasePath(path, project.BasePath)
	if err != nil {
		return Resolved{}, fmt.Errorf("resolving base config path: %w", err)
	}

	merged := project
	if _, statErr := os.Stat(basePath); statErr == nil {
		base, err := loadResolved(ctx, basePath)
		if err != nil {
			return Resolved{}, err
		}
		merged = mergeConfig(base, project)
	} else if !os.IsNotExist(statErr) {
		return Resolved{}, fmt.Errorf("checking base config %s: %w", basePath, statErr)
	}

	if err := validate(merged); err != nil {
		return Resolved{}, err
	}
	return resolved(merged), nil
}

// loadResolved reads path's raw content, injects bivouac's own
// embedded schema as its amends target (path itself must not declare
// one), and evaluates the result with an evaluator pointed at
// pklCacheDir() for its package cache.
func loadResolved(ctx context.Context, path string) (ret Config, err error) {
	// #nosec G304 -- path is the intended bivouac.pkl/base.pkl to load;
	// reading a caller-supplied config path is this function's entire
	// job (see docs/config.md's "A note on trust" -- such a file is
	// already treated as trusted, script-equivalent input).
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("loading %s: %w", path, err)
	}

	tmpPath, cleanup, err := injectSchema(path, raw)
	if err != nil {
		return Config{}, err
	}
	defer cleanup()

	cacheDir, err := pklCacheDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolving pkl cache directory: %w", err)
	}

	evaluator, err := pkl.NewEvaluator(ctx, pkl.PreconfiguredOptions, func(opts *pkl.EvaluatorOptions) {
		opts.CacheDir = cacheDir
	})
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Config{}, fmt.Errorf("pkl CLI not found on PATH (run inside `nix develop`, or install it: https://pkl-lang.org/main/current/pkl-cli/index.html#installation): %w", err)
		}
		return Config{}, fmt.Errorf("loading %s: %w", path, err)
	}
	defer func() {
		cerr := evaluator.Close()
		if err == nil {
			err = cerr
		}
	}()

	ret, err = Load(ctx, evaluator, pkl.FileSource(tmpPath))
	if err != nil {
		err = fmt.Errorf("loading %s: %w", path, err)
	}
	return ret, err
}

// mergeConfig layers project over base: scalars use project's value if
// set, else base's; lists are additive, base's entries first.
//
// BasePath is intentionally not carried into the returned Config — it's
// only meaningful for deciding which base file to merge, not as part of
// the merged output.
func mergeConfig(base, project Config) Config {
	return Config{
		Region:    coalesce(project.Region, base.Region),
		Size:      coalesce(project.Size, base.Size),
		Template:  coalesce(project.Template, base.Template),
		Arch:      project.Arch,
		Image:     project.Image,
		Tailscale: project.Tailscale,
		Beads:     project.Beads,
		SshKeys:   mergeStringSlicePtrs(base.SshKeys, project.SshKeys),
		Packages:  append(append([]string{}, base.Packages...), project.Packages...),
		Agents:    append(append([]string{}, base.Agents...), project.Agents...),
		Flakes:    append(append([]Flake{}, base.Flakes...), project.Flakes...),
		HerdrTabs: append(append([]HerdrTab{}, base.HerdrTabs...), project.HerdrTabs...),
		Settings:  mergeSettings(base.Settings, project.Settings),
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

// mergeSettings layers project over base by key: the project's value
// replaces any matching base key and adds any new one. Unlike every
// Listing field above, this is not additive -- a settings value is a
// single override, not an accumulating list, so keeping both a base and
// a project value for the same key would mean picking one arbitrarily
// at render time instead of deciding it here, once.
func mergeSettings(base, project map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(project))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range project {
		merged[k] = v
	}
	return merged
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
