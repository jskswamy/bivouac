package config

import (
	"github.com/jskswamy/cloudlab/internal/xdg"
)

// pklCacheDir returns the directory the Pkl evaluator should cache
// downloaded `package:` modules in. See internal/xdg for the rule.
func pklCacheDir() (string, error) {
	return xdg.Path(xdg.Cache, "pkl")
}
