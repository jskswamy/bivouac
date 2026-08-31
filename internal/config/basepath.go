package config

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveBasePath determines the personal base config's path for a
// project file at projectPath. Resolution order: override (if set and
// non-empty) — ~-expanded if home-relative, used as-is if absolute,
// else resolved relative to projectPath's own directory; else
// $XDG_CONFIG_HOME/cloudlab/base.pkl; else ~/.config/cloudlab/base.pkl.
func resolveBasePath(projectPath string, override *string) (string, error) {
	if override != nil && *override != "" {
		return resolveOverridePath(filepath.Dir(projectPath), *override)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cloudlab", "base.pkl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cloudlab", "base.pkl"), nil
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
