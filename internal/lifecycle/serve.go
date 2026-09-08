package lifecycle

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// ServeEntry is one port published on the tailnet by `tailscale serve`.
type ServeEntry struct {
	Port    int
	Forward string
}

// serveStatusJSON is the shape `tailscale serve status --json` returns.
// Only the TCP section is read: cloudlab publishes with --tcp, and the
// HTTP/HTTPS sections describe a different kind of entry it does not
// create. An empty config is "{}", so every field must tolerate absence.
type serveStatusJSON struct {
	TCP map[string]struct {
		TCPForward string
	}
}

// parseServeStatus turns that JSON into entries, sorted by port.
//
// Sorted because Go randomises map iteration: unsorted, the numbered
// picker would list the same entries in a different order on each run,
// so choice 1 would not mean the same thing twice.
func parseServeStatus(jsonOut string) ([]ServeEntry, error) {
	var raw serveStatusJSON
	if err := json.Unmarshal([]byte(jsonOut), &raw); err != nil {
		return nil, fmt.Errorf("parsing `tailscale serve status --json`: %w", err)
	}
	var entries []ServeEntry
	for portStr, v := range raw.TCP {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		entries = append(entries, ServeEntry{Port: port, Forward: v.TCPForward})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Port < entries[j].Port })
	return entries, nil
}

// serveArgs publishes the instance's own localhost:port on the tailnet.
//
// --bg because the point is to outlive the command; without it tailscale
// holds the terminal, which is what the ssh forward already does and
// what this exists to avoid.
func serveArgs(bin string, port int) string {
	return fmt.Sprintf("sudo %s serve --bg --tcp %d tcp://localhost:%d", bin, port, port)
}

// unserveArgs removes exactly one entry.
//
// Per-entry rather than `serve reset`: reset clears every entry on the
// instance, including any the user set up by hand, and `serve status`
// does not record which ones cloudlab added.
func unserveArgs(bin string, port int) string {
	return fmt.Sprintf("sudo %s serve --tcp %d off", bin, port)
}
