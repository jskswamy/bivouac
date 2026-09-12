// Package xdg resolves the directories cloudlab keeps its own files in,
// following the XDG Base Directory specification.
//
// One of the two leaf packages (with internal/shellcmd) that the rest of
// the tree may import freely: stdlib only, so even internal/identity --
// which imports no internal package at all -- could use it. The rule it
// owns was written out four times before this existed, in config, secrets
// and state, and two of those copies carried doc comments naming another
// copy as the thing they were following.
package xdg

import (
	"os"
	"path/filepath"
)

// Base is one of the XDG base directories cloudlab stores things under.
type Base int

const (
	// Config -- user configuration. $XDG_CONFIG_HOME, else ~/.config.
	Config Base = iota
	// Cache -- regenerable data. $XDG_CACHE_HOME, else ~/.cache.
	Cache
	// State -- data that persists between runs but is not configuration.
	// $XDG_STATE_HOME, else ~/.local/state, which is two segments rather
	// than one dotted directory like the others.
	State
)

// env is the environment variable that overrides b's location.
func (b Base) env() string {
	switch b {
	case Config:
		return "XDG_CONFIG_HOME"
	case Cache:
		return "XDG_CACHE_HOME"
	case State:
		return "XDG_STATE_HOME"
	}
	return ""
}

// fallback is b's location under $HOME when the environment says nothing,
// as path segments because State's is ~/.local/state rather than a single
// dotted directory.
func (b Base) fallback() []string {
	switch b {
	case Config:
		return []string{".config"}
	case Cache:
		return []string{".cache"}
	case State:
		return []string{".local", "state"}
	}
	return nil
}

// Path returns the path to parts inside cloudlab's own directory under b:
// $XDG_<KIND>_HOME/cloudlab/<parts...> when that variable is set to a
// non-empty value, else $HOME/<fallback>/cloudlab/<parts...>. The
// "cloudlab" segment is added here, never by the caller.
//
// Linux-shaped on every OS, which is what all four of the resolvers this
// replaces already did.
//
// An empty variable counts as unset, not as the empty path. Every copy of
// this rule tested the value rather than its presence, and cloudlab's
// tests set XDG_* to "" to reach the fallback -- so os.LookupEnv here
// would be a silent behaviour change.
func Path(b Base, parts ...string) (string, error) {
	if dir := os.Getenv(b.env()); dir != "" {
		return filepath.Join(append([]string{dir, "cloudlab"}, parts...)...), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	segments := append([]string{home}, b.fallback()...)
	segments = append(segments, "cloudlab")
	return filepath.Join(append(segments, parts...)...), nil
}
