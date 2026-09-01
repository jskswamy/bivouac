package config

import (
	"os"
	"path/filepath"
)

// pklCacheDir returns the directory the Pkl evaluator should cache
// downloaded `package:` modules in: $XDG_CACHE_HOME/cloudlab/pkl if
// set, else ~/.cache/cloudlab/pkl (Linux-shaped on every OS, matching
// resolveBasePath's XDG convention elsewhere in this package).
func pklCacheDir() (string, error) {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "cloudlab", "pkl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "cloudlab", "pkl"), nil
}
