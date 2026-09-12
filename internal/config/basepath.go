package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jskswamy/cloudlab/internal/xdg"
)

// resolveBasePath determines the personal base config's path for a
// project file at projectPath. The override wins when set and non-empty
// — ~-expanded if home-relative, used as-is if absolute, else resolved
// relative to projectPath's own directory. Otherwise base.pkl comes from
// cloudlab's config directory; see internal/xdg for that rule.
func resolveBasePath(projectPath string, override *string) (string, error) {
	if override != nil && *override != "" {
		return resolveOverridePath(filepath.Dir(projectPath), *override)
	}
	return xdg.Path(xdg.Config, "base.pkl")
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
