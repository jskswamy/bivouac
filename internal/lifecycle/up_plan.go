package lifecycle

import (
	"context"
	"fmt"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/identity"
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
func PrepareUp(ctx context.Context, name, configPath, repoRoot string, cfg config.Resolved, keys keyplan.Plan) (UpPlan, error) {
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
	cloudInit, err := provisioning.RenderCloudInit(user, cfg.Shell, provisioning.BootStock)
	if err != nil {
		return UpPlan{}, err
	}
	alias, err := sshidentity.Alias(name)
	if err != nil {
		return UpPlan{}, err
	}
	connection := sshidentity.Connection{Name: name, Alias: alias, PublicIP: "192.0.2.1", PreferredHost: "192.0.2.1", User: user, Keys: append([]sshidentity.Identity(nil), keys.Identities...)}
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		return UpPlan{}, err
	}
	if err := profiles.Check(ctx, connection); err != nil {
		return UpPlan{}, err
	}
	return UpPlan{Name: name, ConfigPath: configPath, RepoRoot: repoRoot, Config: cfg, Keys: keys, Connection: connection, RemotePath: remotePath, Spec: provider.InstanceSpec{Name: name, Region: cfg.Region, Size: cfg.Size, Image: cfg.Image, UserData: cloudInit}}, nil
}
