package lifecycle

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provisioning"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/jskswamy/cloudlab/internal/tui"
)

const readyTimeout = 5 * time.Minute

// validInstanceName matches names Mutagen's sync-session naming
// accepts: must start with a letter, followed by letters, digits, or
// hyphens. Checked before any expensive step (VM creation, billing)
// begins.
var validInstanceName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// Steps groups the lifecycle steps Up calls after creating the VM, so
// tests can substitute recording fakes for the steps that would
// otherwise need a real remote rsync/mutagen target. Production
// callers always use DefaultSteps.
type Steps struct {
	WaitReady     func(ctx context.Context, ip string, timeout time.Duration) error
	Reconcile     func(ctx context.Context, name, cloudlabPath string) error
	JoinTailscale func(ctx context.Context, ip, user string) error
	Rsync         func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error
	StartWatch    func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error
}

// DefaultSteps wires Steps to the real implementations. Reconcile is
// wrapped in tui.Run so home-manager switch's output renders in a
// collapsible viewport (on a real terminal; plain streaming otherwise)
// -- reconcile.Reconcile itself is unchanged either way.
func DefaultSteps() Steps {
	return Steps{
		WaitReady: WaitReady,
		Reconcile: func(ctx context.Context, name, cloudlabPath string) error {
			return tui.Run(ctx, "Reconciling environment", func(ctx context.Context) error {
				return reconcile.Reconcile(ctx, name, cloudlabPath)
			})
		},
		JoinTailscale: JoinTailscale,
		Rsync:         Rsync,
		StartWatch:    StartWatch,
	}
}

// Up creates a new instance and brings it fully live: creates the VM,
// records its state immediately (so a later failure still leaves it
// destroyable), waits until it's genuinely ready, reconciles
// home-manager once, rsyncs the repo in, and starts continuous watch.
func Up(ctx context.Context, p provider.Provider, steps Steps, name, cloudlabPath, repoRoot string) error {
	if !validInstanceName.MatchString(name) {
		return fmt.Errorf("instance name %q is not valid (must start with a letter, and contain only letters, digits, and hyphens -- required by the watch session name) -- pass an explicit --name", name)
	}

	cfg, err := config.Resolve(ctx, cloudlabPath)
	if err != nil {
		return err
	}

	var sshKeys []string
	if cfg.SshKeys != nil {
		sshKeys = *cfg.SshKeys
	}

	// remoteUser is the instance's own non-root login, derived once
	// here and stored in its state record -- not re-derived per
	// command, since a later command may run as a different local user
	// or on a different machine and must keep talking to whichever
	// user this instance was actually provisioned with.
	remoteUser, err := identity.RemoteUser()
	if err != nil {
		return fmt.Errorf("deriving remote username: %w", err)
	}
	// remotePath is likewise computed once and stored (state.Record.RepoPath)
	// rather than re-derived: a later `cloudlab ssh` may run from
	// somewhere that isn't this repo's checkout at all, and needs to
	// know where THIS instance's repo actually landed.
	remotePath, err := RemotePath(repoRoot, remoteUser)
	if err != nil {
		return fmt.Errorf("computing remote repo path: %w", err)
	}
	cloudInit, err := provisioning.RenderCloudInit(remoteUser)
	if err != nil {
		return fmt.Errorf("rendering cloud-init: %w", err)
	}

	spec := provider.InstanceSpec{
		Name:     name,
		Region:   *cfg.Region,
		Size:     *cfg.Size,
		Image:    cfg.Image,
		SSHKeys:  sshKeys,
		UserData: cloudInit,
	}

	store, err := state.Open()
	if err != nil {
		return err
	}

	provider.ReportProgress(ctx, "creating instance")
	vm, err := p.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	record := state.Record{
		Name:     name,
		Provider: "digitalocean",
		VMID:     vm.ID,
		IP:       vm.IP,
		Region:   vm.Region,
		Size:     vm.Size,
		Template: *cfg.Template,
		User:     remoteUser,
		RepoPath: remotePath,
	}
	if err := store.Put(record); err != nil {
		if destroyErr := p.Destroy(ctx, vm.ID); destroyErr != nil {
			return fmt.Errorf("recording instance state failed (%v), and cleanup also failed -- destroy VM %s manually: %w", err, vm.ID, destroyErr)
		}
		return fmt.Errorf("recording instance state: %w", err)
	}

	// WaitReady always connects as root (see its own doc comment) --
	// every step after it connects as remoteUser instead, once
	// cloud-init has created that user and (as its last step) disabled
	// root SSH login.
	if err := steps.WaitReady(ctx, vm.IP, readyTimeout); err != nil {
		return fmt.Errorf("waiting for %s to be ready: %w", name, err)
	}

	if err := steps.Reconcile(ctx, name, cloudlabPath); err != nil {
		return err
	}

	if cfg.Tailscale {
		if err := steps.JoinTailscale(ctx, vm.IP, remoteUser); err != nil {
			return err
		}
		record.TailscaleJoined = true
		if err := store.Put(record); err != nil {
			return err
		}
	}

	if err := steps.Rsync(ctx, vm.IP, remoteUser, repoRoot, remotePath); err != nil {
		return err
	}

	if err := steps.StartWatch(ctx, vm.IP, remoteUser, name, repoRoot, remotePath); err != nil {
		return err
	}

	return nil
}
