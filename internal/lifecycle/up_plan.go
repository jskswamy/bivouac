package lifecycle

import (
	"context"
	"fmt"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/identity"
	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/keyplan"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provisioning"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
)

type UpPlan struct {
	Name, ConfigPath, RepoRoot string
	Config                     config.Resolved
	Spec                       provider.InstanceSpec
	Keys                       keyplan.Plan
	Connection                 sshidentity.Connection
	RemotePath                 string
	// Image is the launch image resolved before approval, so the boot
	// script and Spec.Image agree with what the user confirmed.
	Image image.Resolution
}

func checkNewInstance(store *state.Store, name string) error {
	if !validInstanceName.MatchString(name) {
		return fmt.Errorf("instance name %q is not valid (must start with a letter, and contain only letters, digits, and hyphens) -- pass an explicit --name", name)
	}
	if existing, ok, err := store.Get(name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("instance %q already exists at %s (id %s); run `bivouac provision %s` to finish setting it up, or `bivouac down %s` first", name, existing.IP, existing.VMID, name, name)
	}
	return nil
}

// PrepareUp settles local creation prerequisites before confirmation or writes.
// Its resolved configuration and key plan are the execution inputs, not paths
// to re-read into a potentially different instance spec after approval.
//
// img is resolved by the caller, which holds the provider; PrepareUp holds
// none. It must already be resolved because it decides the boot script: a
// baked image gets access setup only, a stock one the full bootstrap.
func PrepareUp(ctx context.Context, name, configPath, repoRoot string, cfg config.Resolved, keys keyplan.Plan, img image.Resolution) (UpPlan, error) {
	mode, err := bootMode(cfg, img)
	if err != nil {
		return UpPlan{}, err
	}
	store, err := state.Open()
	if err != nil {
		return UpPlan{}, err
	}
	if err := checkNewInstance(store, name); err != nil {
		return UpPlan{}, err
	}
	if len(keys.Entries) == 0 || len(keys.Identities) == 0 {
		return UpPlan{}, fmt.Errorf("instance creation needs selected public keys and a usable local signing capability")
	}
	user, err := identity.RemoteUser()
	if err != nil {
		return UpPlan{}, fmt.Errorf("deriving remote username: %w", err)
	}
	remotePath, err := RemotePath(repoRoot, user)
	if err != nil {
		return UpPlan{}, err
	}
	cloudInit, err := provisioning.RenderCloudInit(user, cfg.Shell, mode)
	if err != nil {
		return UpPlan{}, err
	}
	alias, err := sshidentity.Alias(name)
	if err != nil {
		return UpPlan{}, err
	}
	connection := sshidentity.Connection{Name: name, Alias: alias, PublicIP: "192.0.2.1", PreferredHost: "192.0.2.1", User: user, Keys: append([]sshidentity.Identity(nil), keys.Identities...)}
	if cfg.Provider == "gcloud" && (cfg.GCloud != nil && !cfg.GCloud.ExternalIP) {
		connection.UseIAP = true
		connection.Project = cfg.GCloud.Project
		connection.Zone = cfg.GCloud.Zone
	}
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		return UpPlan{}, err
	}
	if err := profiles.Check(ctx, connection); err != nil {
		return UpPlan{}, err
	}
	return UpPlan{Name: name, ConfigPath: configPath, RepoRoot: repoRoot, Config: cfg, Keys: keys, Connection: connection, RemotePath: remotePath, Image: img, Spec: provider.InstanceSpec{Name: name, Region: cfg.Region, Size: cfg.Size, Image: img.Image, UserData: cloudInit, User: user}}, nil
}

// bootMode checks img against cfg and picks the boot script for it.
//
// The shell check is a hard stop. A baked image's login shell was set when
// it was built and the baked boot path only detects it, so a config asking
// for another shell would boot with the wrong one -- or need a second
// shell install at launch, which is the slow path the image exists to
// skip. Naming both lets the user pick which side to change.
func bootMode(cfg config.Resolved, img image.Resolution) (provisioning.BootMode, error) {
	if img.Requested != cfg.Image || img.Image == "" {
		return 0, fmt.Errorf("image resolution %q -> %q does not match the configured image %q", img.Requested, img.Image, cfg.Image)
	}
	if !img.Baked {
		return provisioning.BootStock, nil
	}
	if img.Shell != cfg.Shell {
		return 0, fmt.Errorf("image %s (%s) was built with login shell %s but the config asks for %s; set shell = %q or build an image for %s", img.Image, img.Requested, img.Shell, cfg.Shell, img.Shell, cfg.Shell)
	}
	return provisioning.BootBaked, nil
}
