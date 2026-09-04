// Package provider defines the VM-lifecycle abstraction every cloud
// provider implementation satisfies, plus the value types and
// cross-cutting helpers (progress reporting) shared across them.
package provider

import (
	"context"
	"errors"
	"io"
	"os"
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
type VM struct {
	ID     string
	Name   string
	IP     string
	Region string
	Size   string
	Status string
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
