package lifecycle

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jskswamy/bivouac/internal/keyplan"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/tui"
)

const readyTimeout = 5 * time.Minute

// Names become provider names, branch components and directory paths on both
// machines. Validate before any irreversible or billable operation.
var validInstanceName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
var validSessionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

func CheckSessionName(session string) error {
	if !validSessionName.MatchString(session) {
		return fmt.Errorf("session name %q is not valid (must start with a letter, and contain only letters, digits, and hyphens)", session)
	}
	return nil
}

// Steps keeps post-creation network operations replaceable in local tests.
type Steps struct {
	WaitReady     func(context.Context, string, string, time.Duration) error
	Reconcile     func(context.Context, string, string) error
	JoinTailscale func(context.Context, string, string) error
	TailnetHost   func(context.Context, string, string) (string, error)
}

func DefaultSteps() Steps {
	return Steps{WaitReady: WaitReady, Reconcile: func(ctx context.Context, name, path string) error {
		return tui.Run(ctx, "Reconciling environment", func(ctx context.Context) error { return reconcile.Reconcile(ctx, name, path) })
	}, JoinTailscale: JoinTailscale, TailnetHost: TailscaleIP}
}

// Up executes only an approved creation plan. Account additions are retained
// on failure; a created VM is tracked before readiness, profile publication or
// provisioning, so later failures cannot leave an invisible billable machine.
func Up(ctx context.Context, p provider.Provider, steps Steps, plan UpPlan) error {
	store, err := state.Open()
	if err != nil {
		return err
	}
	// Recheck even after PrepareUp: another process may have completed creation
	// while the user was considering the confirmation prompt.
	if err := checkNewInstance(store, plan.Name); err != nil {
		return err
	}
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		return err
	}
	if err := profiles.Check(ctx, plan.Connection); err != nil {
		return err
	}
	registry, ok := p.(provider.KeyRegistry)
	if !ok {
		return fmt.Errorf("provider cannot register selected SSH keys")
	}
	ids, err := keyplan.Register(ctx, registry, plan.Keys)
	if err != nil {
		return err
	}
	spec := plan.Spec
	spec.SSHKeys = ids
	provider.ReportProgress(ctx, "creating instance")
	vm, err := p.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("creating instance (approved account additions retained): %w", err)
	}
	connection := plan.Connection
	connection.PublicIP = vm.IP
	connection.PreferredHost = vm.IP
	if plan.Config.Provider == "gcloud" && (plan.Config.GCloud != nil && !plan.Config.GCloud.ExternalIP) {
		connection.UseIAP = true
		connection.Project = plan.Config.GCloud.Project
		connection.Zone = plan.Config.GCloud.Zone
	}
	providerName := plan.Config.Provider
	if providerName == "" {
		providerName = "digitalocean"
	}
	var project string
	if plan.Config.GCloud != nil {
		project = plan.Config.GCloud.Project
	}
	record := state.Record{
		Name: plan.Name, Provider: providerName, Project: project, VMID: vm.ID, IP: vm.IP,
		Region: vm.Region, Size: vm.Size, Template: plan.Config.Template, User: connection.User,
		RepoPath: plan.RemotePath, Shell: plan.Config.Shell, SSH: &connection, ImageRef: plan.Image.Requested,
	}
	if plan.Image.Baked {
		record.ImageID = plan.Image.Image
	}
	if err := store.Put(record); err != nil {
		if destroyErr := p.Destroy(ctx, vm.ID); destroyErr != nil {
			return fmt.Errorf("recording instance state failed (%v), and cleanup also failed -- destroy VM %s manually: %w", err, vm.ID, destroyErr)
		}
		return fmt.Errorf("recording instance state: %w", err)
	}
	if err := profiles.Upsert(ctx, connection); err != nil {
		return fmt.Errorf("instance %s is tracked but SSH profile installation failed: %w; repair the conflicting local SSH configuration and retry bivouac provision %s", plan.Name, err, plan.Name)
	}
	ctx = sshidentity.WithConnection(ctx, connection)
	if err := steps.WaitReady(ctx, vm.IP, connection.User, readyTimeout); err != nil {
		return fmt.Errorf("waiting for %s to be ready: %w", plan.Name, err)
	}
	if err := steps.Reconcile(ctx, plan.Name, plan.ConfigPath); err != nil {
		return err
	}
	if plan.Config.Tailscale {
		if err := steps.JoinTailscale(ctx, vm.IP, connection.User); err != nil {
			return err
		}
		if err := store.Update(record.Name, func(latest *state.Record) error {
			latest.TailscaleJoined = true
			return nil
		}); err != nil {
			return err
		}
		if steps.TailnetHost != nil {
			host, err := steps.TailnetHost(ctx, vm.IP, connection.User)
			if err != nil {
				return err
			}
			if _, err := rememberPreferredHost(ctx, host); err != nil {
				return err
			}
		}
	}
	return nil
}
