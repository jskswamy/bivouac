// Package sshidentity shares selected authentication metadata across instance
// transports. Descriptors contain references and public identity, never secrets.
package sshidentity

import "context"

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
