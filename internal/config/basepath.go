package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jskswamy/cloudlab/internal/xdg"
)

// Dir returns cloudlab's own configuration directory:
// $XDG_CONFIG_HOME/cloudlab if set, else ~/.config/cloudlab; see
// internal/xdg for that rule.
//
// Exported because the personal base config is no longer the only thing
// that lives there — presets sit alongside it, and a second copy of
// this rule is a second thing to get wrong.
func Dir() (string, error) {
	return xdg.Path(xdg.Config)
}

// DefaultBasePath returns where the personal base config lives when a
// project file does not point somewhere else: Dir()/base.pkl.
func DefaultBasePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "base.pkl"), nil
}

// resolveBasePath determines the personal base config's path for a
// project file at projectPath. Resolution order: override (if set and
// non-empty) — ~-expanded if home-relative, used as-is if absolute,
// else resolved relative to projectPath's own directory; else
// DefaultBasePath.
func resolveBasePath(projectPath string, override *string) (string, error) {
	if override != nil && *override != "" {
		return resolveOverridePath(filepath.Dir(projectPath), *override)
	}
	return DefaultBasePath()
}

func resolveOverridePath(projectDir, override string) (string, error) {
	if override == "~" || strings.HasPrefix(override, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(override, "~")), nil
	}
	if filepath.IsAbs(override) {
		return override, nil
	}
	return filepath.Join(projectDir, override), nil
}
