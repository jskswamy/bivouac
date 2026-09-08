package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jskswamy/cloudlab/internal/reconcile"
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
//
// jsonOut is `client.Run`'s combined stdout+stderr, not guaranteed-clean
// JSON: a routine stderr line (e.g. "sudo: unable to resolve host ...")
// that doesn't change the exit status would otherwise make Unmarshal fail
// and report a perfectly reachable instance as unreachable. Every other
// consumer of Run in this package tolerates the same noise (parseListeners
// skips non-LISTEN lines, TailscaleIP takes the first line), so this slices
// from the first `{` before parsing rather than trusting the output is bare.
func parseServeStatus(jsonOut string) ([]ServeEntry, error) {
	if i := strings.IndexByte(jsonOut, '{'); i >= 0 {
		jsonOut = jsonOut[i:]
	}
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
	return fmt.Sprintf("sudo %s serve --bg --tcp %d tcp://localhost:%d", reconcile.ShellQuote(bin), port, port)
}

// unserveArgs removes exactly one entry.
//
// Per-entry rather than `serve reset`: reset clears every entry on the
// instance, including any the user set up by hand, and `serve status`
// does not record which ones cloudlab added.
func unserveArgs(bin string, port int) string {
	return fmt.Sprintf("sudo %s serve --tcp %d off", reconcile.ShellQuote(bin), port)
}

// serveSession connects, resolves the tailscale binary, and hands both
// to fn. All three exported calls need exactly this preamble, and
// resolving the binary rather than assuming a path is what makes them
// work on an instance where tailscale came from nix.
func serveSession(ctx context.Context, ip, user string, fn func(client *reconcile.Client, bin string) error) error {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	bin, err := RemoteTailscaleBin(client)
	if err != nil {
		return err
	}
	return fn(client, bin)
}

// Serve publishes the instance's localhost:port on the tailnet.
//
// sudo: tailscaled runs as root and gates serve config changes on it;
// cloud-init grants this user passwordless sudo (see cloud-init.sh).
func Serve(ctx context.Context, ip, user string, port int) error {
	return serveSession(ctx, ip, user, func(client *reconcile.Client, bin string) error {
		if out, err := client.Run("bash -lc " + reconcile.ShellQuote(serveArgs(bin, port))); err != nil {
			return fmt.Errorf("serving port %d on the tailnet: %w\n%s", port, err, out)
		}
		return nil
	})
}

// Unserve stops publishing one port, leaving every other entry alone.
func Unserve(ctx context.Context, ip, user string, port int) error {
	return serveSession(ctx, ip, user, func(client *reconcile.Client, bin string) error {
		if out, err := client.Run("bash -lc " + reconcile.ShellQuote(unserveArgs(bin, port))); err != nil {
			return fmt.Errorf("stopping serve on port %d: %w\n%s", port, err, out)
		}
		return nil
	})
}

// ServeStatus reports every port published on the instance, including
// entries cloudlab did not create -- tailscale does not record who added
// one, and showing only a subset would make `unserve` look broken.
func ServeStatus(ctx context.Context, ip, user string) ([]ServeEntry, error) {
	var entries []ServeEntry
	err := serveSession(ctx, ip, user, func(client *reconcile.Client, bin string) error {
		out, err := client.Run("bash -lc " + reconcile.ShellQuote("sudo "+reconcile.ShellQuote(bin)+" serve status --json"))
		if err != nil {
			return fmt.Errorf("reading serve status: %w\n%s", err, out)
		}
		parsed, err := parseServeStatus(out)
		if err != nil {
			return err
		}
		entries = parsed
		return nil
	})
	return entries, err
}
