package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provider"
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
		provider.ReportProgress(ctx, "shipping per-instance flake")
		content, err := provisioning.Render(cfg, templateRef)
		if err != nil {
			return fmt.Errorf("rendering per-instance flake: %w", err)
		}
		if err := client.WriteFile(remoteFlakePath, content); err != nil {
			return fmt.Errorf("shipping per-instance flake: %w", err)
		}
		flakeArg = "path:" + remoteFlakeDir + "#default"
	}

	provider.ReportProgress(ctx, "reconciling environment (home-manager switch)")

	// --refresh forces Nix to re-fetch flake inputs rather than serve a
	// floating github: ref from its tarball cache (default TTL: 1
	// hour) -- without it, a just-pushed template fix wouldn't take
	// effect on an existing instance for up to an hour.
	innerCmd := "nix run home-manager -- switch --no-write-lock-file --refresh --flake " + shellQuote(flakeArg)
	cmd := "bash -lc " + shellQuote(innerCmd)
	// Streamed live (not buffered until exit): home-manager switch can
	// run for minutes fetching/building packages, and silence until
	// completion is indistinguishable from a hang. Writers come from
	// ctx (provider.Output), defaulting to os.Stdout/os.Stderr -- a
	// caller can redirect them (e.g. into a bubbletea viewport)
	// without this function needing to know or care.
	out, errOut := provider.Output(ctx)
	output, err := client.RunStreaming(cmd, out, errOut)
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
