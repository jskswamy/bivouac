package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provisioning"
	"github.com/jskswamy/cloudlab/internal/state"
)

const (
	remoteFlakeDir  = "/root/.cache/cloudlab"
	remoteFlakePath = remoteFlakeDir + "/flake.nix"
)

// Reconcile brings name's home-manager environment up to date with its
// local cloudlab.pkl at cloudlabPath: resolves the config, renders a
// per-instance wrapper flake if packages/flakes require one, ships it to
// the instance over SSH, and runs home-manager switch. This is the one
// piece up, shell, and provision all share.
func Reconcile(ctx context.Context, name, cloudlabPath string) error {
	store, err := state.Open()
	if err != nil {
		return err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("instance %q not found — run \"cloudlab up %s\" first", name, name)
	}

	cfg, err := config.Resolve(ctx, cloudlabPath)
	if err != nil {
		return err
	}

	templateRef := provisioning.ResolveTemplateRef(*cfg.Template, cfg.Arch)

	client, err := Connect(ctx, record.IP)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", record.IP, err)
	}
	defer func() { _ = client.Close() }()

	flakeArg := templateRef
	if provisioning.NeedsRender(cfg) {
		content, err := provisioning.Render(cfg, templateRef)
		if err != nil {
			return fmt.Errorf("rendering per-instance flake: %w", err)
		}
		if err := client.WriteFile(remoteFlakePath, content); err != nil {
			return fmt.Errorf("shipping per-instance flake: %w", err)
		}
		flakeArg = "path:" + remoteFlakeDir + "#default"
	}

	innerCmd := "nix run home-manager -- switch --no-write-lock-file --flake " + shellQuote(flakeArg)
	cmd := "bash -lc " + shellQuote(innerCmd)
	output, err := client.Run(cmd)
	if err != nil {
		return fmt.Errorf("home-manager switch failed: %w\n%s", err, tail(output, 40))
	}
	return nil
}

// tail returns s's last n lines, unchanged if it has n or fewer.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
