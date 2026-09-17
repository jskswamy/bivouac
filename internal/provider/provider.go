// Package provider defines the VM-lifecycle abstraction every cloud
// provider implementation satisfies, plus the value types and
// cross-cutting helpers (progress reporting) shared across them.
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Provider creates, destroys, and inspects VMs. Only DigitalOcean is
// implemented; provider-specific concepts (droplet size, region, image)
// stay as direct InstanceSpec fields rather than a forced cross-provider
// abstraction.
type Provider interface {
	Create(ctx context.Context, spec InstanceSpec) (VM, error)
	Destroy(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (VM, error)
	List(ctx context.Context) ([]VM, error)
}

// KeyRegistry is implemented by providers that can enumerate and
// register the account's SSH keys.
//
// Deliberately not part of Provider. That interface is the VM lifecycle
// and its doc comment above says provider-specific concepts stay out of
// it rather than being forced into a cross-provider abstraction; key
// management is exactly such a concept, and a provider that
// authenticates some other way would have to stub it. The interactive
// config flow is the only caller, so one type assertion at one call
// site is cheaper than a method every future provider must implement in
// order to say "unsupported".
type KeyRegistry interface {
	ListKeys(ctx context.Context) ([]SSHKey, error)
	CreateKey(ctx context.Context, name, publicKey string) (SSHKey, error)
}

// SSHKey is one key registered on the provider account.
type SSHKey struct {
	ID          string
	Name        string
	Fingerprint string
	PublicKey   string
}

// ErrForbidden reports that the credential is valid but not permitted
// to do this.
//
// Worth its own error because a token scoped without the SSH-key
// permissions is the *recommended* configuration, not a mistake: the
// caller must be able to drop an optional capability rather than fail
// the run. See IsForbidden.
var ErrForbidden = errors.New("not permitted by this token")

// IsForbidden reports whether err is a permission refusal.
func IsForbidden(err error) bool {
	return errors.Is(err, ErrForbidden)
}

// InstanceSpec describes the VM to create.
type InstanceSpec struct {
	Name     string
	Region   string   // e.g. "nyc3"
	Size     string   // e.g. "s-1vcpu-1gb"
	Image    string   // e.g. "ubuntu-22-04-x64"
	SSHKeys  []string // provider SSH key IDs/fingerprints
	UserData string   // cloud-init script, opaque to this package
}

// VM is a created instance's current state.
//
// CreatedAt and the two prices are what an instance costs to keep
// running, carried on the same read that reports Status because
// DigitalOcean returns them inline on the droplet. A provider that
// cannot report one leaves it zero, which every consumer must read as
// "unknown" rather than "free" -- see lifecycle.ComputeCost.
type VM struct {
	ID           string
	Name         string
	IP           string
	Region       string
	Size         string
	Status       string
	CreatedAt    time.Time
	PriceHourly  float64
	PriceMonthly float64
}

// ErrNotFound is returned by Get/Destroy when the VM no longer exists.
var ErrNotFound = errors.New("vm not found")

// ProgressFunc receives human-readable status updates during long-running
// operations (currently: Create's wait for the VM to become ready).
type ProgressFunc func(status string)

type progressKey struct{}

// WithProgress attaches fn to ctx. Provider implementations call
// ReportProgress with the resulting context to report status without
// depending on any UI library.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportProgress calls the ProgressFunc attached to ctx via WithProgress,
// if any. It is a no-op if none was set.
func ReportProgress(ctx context.Context, status string) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		fn(status)
	}
}

type outputKey struct{}

type outputWriters struct {
	out, errOut io.Writer
}

// WithOutput attaches out/errOut to ctx as the destination for a
// subprocess's raw stdout/stderr (currently: Reconcile's home-manager
// switch). Lets a caller redirect that output -- e.g. into a bubbletea
// viewport -- without changing Reconcile's signature or logic.
func WithOutput(ctx context.Context, out, errOut io.Writer) context.Context {
	return context.WithValue(ctx, outputKey{}, outputWriters{out, errOut})
}

// Output returns the stdout/stderr writers attached to ctx via
// WithOutput, or os.Stdout/os.Stderr if none were attached.
func Output(ctx context.Context) (out, errOut io.Writer) {
	if w, ok := ctx.Value(outputKey{}).(outputWriters); ok {
		return w.out, w.errOut
	}
	return os.Stdout, os.Stderr
}

type terminalSuspendKey struct{}

// TerminalSuspender gives up whatever is currently rendering to the
// terminal so a child process can use it for real interactive input
// (a YubiKey PIN prompt, an editor), returning a resume func to call
// once that's done. See WithTerminalSuspend.
type TerminalSuspender func() (resume func() error, err error)

// WithTerminalSuspend attaches fn to ctx. A caller that owns the
// terminal for the duration of a long operation (currently: tui.Run's
// bubbletea program) sets this so any subprocess started underneath
// it can borrow the terminal back rather than fight over raw-mode
// input with the owner's own event loop.
func WithTerminalSuspend(ctx context.Context, fn TerminalSuspender) context.Context {
	return context.WithValue(ctx, terminalSuspendKey{}, fn)
}

// SuspendTerminal calls the TerminalSuspender attached to ctx, if any,
// and returns the resume func to call once the caller is done with
// the terminal. Both the call and the returned resume func are no-ops
// when nothing was attached (e.g. no bubbletea program is currently
// rendering) or the suspend itself failed -- the caller proceeds
// either way, since forwarding whatever raw terminal state already
// exists is strictly better than refusing to run at all.
func SuspendTerminal(ctx context.Context) (resume func() error) {
	fn, ok := ctx.Value(terminalSuspendKey{}).(TerminalSuspender)
	if !ok || fn == nil {
		return func() error { return nil }
	}
	r, err := fn()
	if err != nil || r == nil {
		return func() error { return nil }
	}
	return r
}

// ReportWarning tells the user something went wrong that was not fatal.
//
// Distinct from ReportProgress, which narrates what is happening on the happy
// path: a warning is what a fail-safe step emits when it gives up and carries
// on. Written to the error writer so a caller rendering progress into a
// viewport does not have to filter it back out of the progress stream.
func ReportWarning(ctx context.Context, msg string) {
	_, errOut := Output(ctx)
	_, _ = fmt.Fprintln(errOut, "warning: "+msg)
}
