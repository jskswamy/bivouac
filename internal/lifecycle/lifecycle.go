package lifecycle

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provisioning"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
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
	WaitReady  func(ctx context.Context, ip string, timeout time.Duration) error
	Reconcile  func(ctx context.Context, name, cloudlabPath string) error
	Rsync      func(ctx context.Context, ip, localRepoRoot, remoteName string) error
	StartWatch func(ctx context.Context, ip, name, localRepoRoot string) error
}

// DefaultSteps wires Steps to the real implementations.
func DefaultSteps() Steps {
	return Steps{
		WaitReady:  WaitReady,
		Reconcile:  reconcile.Reconcile,
		Rsync:      Rsync,
		StartWatch: StartWatch,
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

	spec := provider.InstanceSpec{
		Name:     name,
		Region:   *cfg.Region,
		Size:     *cfg.Size,
		Image:    cfg.Image,
		SSHKeys:  sshKeys,
		UserData: provisioning.CloudInitUserData,
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
	}
	if err := store.Put(record); err != nil {
		if destroyErr := p.Destroy(ctx, vm.ID); destroyErr != nil {
			return fmt.Errorf("recording instance state failed (%v), and cleanup also failed -- destroy VM %s manually: %w", err, vm.ID, destroyErr)
		}
		return fmt.Errorf("recording instance state: %w", err)
	}

	if err := steps.WaitReady(ctx, vm.IP, readyTimeout); err != nil {
		return fmt.Errorf("waiting for %s to be ready: %w", name, err)
	}

	if err := steps.Reconcile(ctx, name, cloudlabPath); err != nil {
		return err
	}

	if err := steps.Rsync(ctx, vm.IP, repoRoot, name); err != nil {
		return err
	}

	if err := steps.StartWatch(ctx, vm.IP, name, repoRoot); err != nil {
		return err
	}

	return nil
}
