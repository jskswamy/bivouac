package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// WatchStatus is a snapshot of an instance's watch (Mutagen sync)
// session, as reported by `mutagen sync list`.
type WatchStatus struct {
	Running        bool
	Status         string
	AlphaConnected bool
	BetaConnected  bool
	Conflicts      int
	LastError      string
}

// mutagenListArgs builds the argv GetWatchStatus passes to the
// mutagen binary: a pipe-delimited Go template selecting exactly the
// fields WatchStatus needs, for name's session.
func mutagenListArgs(name string) []string {
	template := "{{range .}}{{.Status}}|{{.Alpha.Connected}}|{{.Beta.Connected}}|{{len .Conflicts}}|{{.LastError}}{{end}}"
	return []string{"sync", "list", name, "--template=" + template}
}

// GetWatchStatus reports name's current watch session state. A
// session that doesn't exist (watch never started, or was stopped) is
// reported as WatchStatus{Running: false}, nil -- not an error. Any
// other mutagen failure (not on PATH, daemon unreachable, a real
// Mutagen-side error) is returned as a genuine error.
func GetWatchStatus(ctx context.Context, name string) (WatchStatus, error) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		return WatchStatus{}, fmt.Errorf("mutagen not found on PATH (run inside `nix develop`, or install it: https://mutagen.io/documentation/introduction/installation): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; name is a local
	// identifier, never attacker-controlled.
	out, err := exec.CommandContext(ctx, "mutagen", mutagenListArgs(name)...).Output()
	if err != nil {
		if isNoSessionError(err) {
			return WatchStatus{Running: false}, nil
		}
		return WatchStatus{}, fmt.Errorf("checking watch status for %s: %w", name, err)
	}
	return parseWatchStatus(strings.TrimSpace(string(out)))
}

// isNoSessionError reports whether err is mutagen's own "no matching
// sessions" exit, distinguishing "watch was never started" from a
// genuine failure (mutagen missing, daemon down, ...).
func isNoSessionError(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return strings.Contains(string(exitErr.Stderr), "did not match any sessions")
}

func parseWatchStatus(line string) (WatchStatus, error) {
	fields := strings.SplitN(line, "|", 5)
	if len(fields) != 5 {
		return WatchStatus{}, fmt.Errorf("unexpected mutagen sync list output: %q", line)
	}
	conflicts, err := strconv.Atoi(fields[3])
	if err != nil {
		return WatchStatus{}, fmt.Errorf("parsing conflict count from mutagen output %q: %w", line, err)
	}
	return WatchStatus{
		Running:        true,
		Status:         fields[0],
		AlphaConnected: fields[1] == "true",
		BetaConnected:  fields[2] == "true",
		Conflicts:      conflicts,
		LastError:      fields[4],
	}, nil
}
