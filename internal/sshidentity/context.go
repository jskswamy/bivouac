// Package sshidentity shares selected authentication metadata across instance
// transports. Descriptors contain references and public identity, never secrets.
package sshidentity

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OverlayProbeTimeout is the maximum time to wait when probing whether an
// instance's Tailscale overlay address is reachable from the local machine
// before falling back to the base transport (IAP tunnel or public IP).
const OverlayProbeTimeout = 2 * time.Second

// negativeOverlayTTL bounds how long a failed overlay probe is remembered
// within a single CLI invocation (and its child subprocesses via
// negativeOverlayEnv) so back-to-back steps (e.g. status, session start,
// up, git + bivouac proxy) do not pay multiple 2-second timeouts against
// the same dead tailnet IP.
const (
	negativeOverlayTTL = 30 * time.Second
	negativeOverlayEnv = "BIVOUAC_NEGATIVE_OVERLAY"
)

var (
	negativeOverlayMu sync.Mutex
	negativeOverlay   = map[string]time.Time{}
)

// Identity records the selected signing mode. Agent is not a socket path:
// each command reacquires the current agent and matches Fingerprint.
type Identity struct {
	PublicPath  string `json:"publicPath"`
	PrivatePath string `json:"privatePath,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Agent       bool   `json:"agent,omitempty"`
}

type Connection struct {
	Name          string     `json:"name"`
	Alias         string     `json:"alias"`
	PublicIP      string     `json:"publicIP"`
	PreferredHost string     `json:"preferredHost"`
	User          string     `json:"user"`
	Keys          []Identity `json:"keys"`
	UseIAP        bool       `json:"useIAP,omitempty"`
	Project       string     `json:"project,omitempty"`
	Zone          string     `json:"zone,omitempty"`
}

// HasOverlay reports whether this connection has a distinct overlay address
// (such as a Tailscale 100.x.y.z IP) separate from its base PublicIP.
func (c Connection) HasOverlay() bool {
	return c.PreferredHost != "" && c.PreferredHost != c.PublicIP
}

// OverlayTarget returns the host:port address for the overlay endpoint, using
// the port from PreferredHost or PublicIP (defaulting to 22).
func (c Connection) OverlayTarget() (string, error) {
	if !c.HasOverlay() {
		return "", fmt.Errorf("connection %s has no overlay address", c.Name)
	}
	host, port, err := endpoint(c.PreferredHost)
	if err != nil {
		return "", err
	}
	if _, _, splitErr := net.SplitHostPort(c.PreferredHost); splitErr != nil && c.PublicIP != "" {
		if _, pubPort, pubErr := endpoint(c.PublicIP); pubErr == nil {
			port = pubPort
		}
	}
	return net.JoinHostPort(host, port), nil
}

// DialOverlay dials the connection's overlay endpoint with OverlayProbeTimeout.
// If the same target failed within negativeOverlayTTL in this process or a
// parent bivouac process, it returns immediately without stalling another 2 seconds.
func (c Connection) DialOverlay() (net.Conn, error) {
	conn, _, err := c.DialOverlayWithStatus()
	return conn, err
}

// DialOverlayWithStatus dials the connection's overlay endpoint and reports
// whether a failure came from the negative-probe cache (cached == true) rather
// than a fresh network timeout.
func (c Connection) DialOverlayWithStatus() (net.Conn, bool, error) {
	target, err := c.OverlayTarget()
	if err != nil {
		return nil, false, err
	}
	if isNegativeOverlayCached(target, time.Now()) {
		return nil, true, fmt.Errorf("overlay %s cached as unreachable", target)
	}

	conn, err := net.DialTimeout("tcp", target, OverlayProbeTimeout)
	if err != nil {
		markNegativeOverlay(target, time.Now().Add(negativeOverlayTTL))
		return nil, false, err
	}
	clearNegativeOverlay(target)
	return conn, false, nil
}

func isNegativeOverlayCached(target string, now time.Time) bool {
	negativeOverlayMu.Lock()
	defer negativeOverlayMu.Unlock()
	syncNegativeOverlayFromEnvLocked(now)
	until, ok := negativeOverlay[target]
	return ok && now.Before(until)
}

func markNegativeOverlay(target string, until time.Time) {
	negativeOverlayMu.Lock()
	defer negativeOverlayMu.Unlock()
	now := time.Now()
	syncNegativeOverlayFromEnvLocked(now)
	negativeOverlay[target] = until
	writeNegativeOverlayEnvLocked(now)
}

func clearNegativeOverlay(target string) {
	negativeOverlayMu.Lock()
	defer negativeOverlayMu.Unlock()
	now := time.Now()
	syncNegativeOverlayFromEnvLocked(now)
	delete(negativeOverlay, target)
	writeNegativeOverlayEnvLocked(now)
}

func syncNegativeOverlayFromEnvLocked(now time.Time) {
	raw := os.Getenv(negativeOverlayEnv)
	if raw == "" {
		return
	}
	for _, entry := range strings.Split(raw, ",") {
		target, millisStr, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || target == "" {
			continue
		}
		millis, err := strconv.ParseInt(millisStr, 10, 64)
		if err != nil {
			continue
		}
		until := time.UnixMilli(millis)
		if now.Before(until) {
			if existing, exists := negativeOverlay[target]; !exists || until.After(existing) {
				negativeOverlay[target] = until
			}
		}
	}
}

func writeNegativeOverlayEnvLocked(now time.Time) {
	var parts []string
	for target, until := range negativeOverlay {
		if !now.Before(until) {
			delete(negativeOverlay, target)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", target, until.UnixMilli()))
	}
	if len(parts) == 0 {
		_ = os.Unsetenv(negativeOverlayEnv)
		return
	}
	_ = os.Setenv(negativeOverlayEnv, strings.Join(parts, ","))
}

// OverlayReachable probes whether the overlay endpoint accepts TCP connections.
func (c Connection) OverlayReachable() bool {
	conn, err := c.DialOverlay()
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

type connectionKey struct{}

func WithConnection(ctx context.Context, c Connection) context.Context {
	c.Keys = append([]Identity(nil), c.Keys...)
	return context.WithValue(ctx, connectionKey{}, c)
}

// WithoutConnection prevents an inherited identity from leaking into an old
// record or the next machine in a multi-instance operation.
func WithoutConnection(ctx context.Context) context.Context {
	return context.WithValue(ctx, connectionKey{}, nil)
}

func FromContext(ctx context.Context) (Connection, bool) {
	c, ok := ctx.Value(connectionKey{}).(Connection)
	c.Keys = append([]Identity(nil), c.Keys...)
	return c, ok
}

type progressKey struct{}

// WithProgress reports potentially interactive agent signing before waiting for
// hardware. It is separate from provider progress to keep this package independent.
func WithProgress(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}
