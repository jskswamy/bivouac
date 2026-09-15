package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jskswamy/cloudlab/internal/agentcontext"
	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provisioning"
	"github.com/jskswamy/cloudlab/internal/shellcmd"
	"github.com/jskswamy/cloudlab/internal/state"
)

// remoteNix runs nix on the instance, for provisioning.Validate.
//
// The instance is the machine that builds, so it is the machine whose
// answer counts: its nixpkgs, its system, its cache. It is also the
// reason cloudlab itself needs no nix installed -- see
// provisioning.Runner.
type remoteNix struct{ client *Client }

// RunNix implements provisioning.Runner.
func (r remoteNix) RunNix(ctx context.Context, args ...string) ([]byte, error) {
	// A login shell for the same reason the switch below uses one: nix is
	// on the login shell's PATH, not a bare exec session's.
	out, err := r.client.RunContext(ctx, shellcmd.Remote([]string{"nix"}, args...))
	return []byte(out), err
}

// remoteFlakeDir is the per-instance wrapper flake's directory, under
// the reconciling user's own home -- not a fixed path, since that user
// varies per instance (see state.Record.User).
func remoteFlakeDir(user string) string {
	return "/home/" + user + "/.cache/cloudlab"
}

func remoteFlakePath(user string) string {
	return remoteFlakeDir(user) + "/flake.nix"
}

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

	templateRef := provisioning.ResolveTemplateRef(cfg.Template, cfg.Arch)

	client, err := Connect(ctx, record.IP, record.User)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", record.IP, err)
	}
	defer func() { _ = client.Close() }()

	// Before the switch, which runs for minutes: an instructions file the
	// config names but the disk does not have is a typo, and a typo should
	// surface now rather than after a build.
	if err := WriteAgentContext(ctx, client, record.User, cfg.Agents, cloudlabPath); err != nil {
		return err
	}

	// Pre-flight, before anything is written and before the switch. A
	// mistyped package name is otherwise a Nix evaluation error part-way
	// through a build that has already been running for minutes, and the
	// message names an attribute path rather than the line the user
	// typed.
	//
	// On the instance rather than here: that is where every build
	// happens, so it is the only nix whose answer is the one that will
	// matter -- and cloudlab asks for no nix on the user's own machine.
	provider.ReportProgress(ctx, "checking the config resolves")
	if err := provisioning.Validate(ctx, remoteNix{client}, cfg); err != nil {
		return err
	}

	flakeArg := templateRef
	if provisioning.NeedsRender(cfg.Config) {
		provider.ReportProgress(ctx, "shipping per-instance flake")
		content, err := provisioning.Render(cfg.Config, templateRef)
		if err != nil {
			return fmt.Errorf("rendering per-instance flake: %w", err)
		}
		if err := client.WriteFile(remoteFlakePath(record.User), content); err != nil {
			return fmt.Errorf("shipping per-instance flake: %w", err)
		}
		flakeArg = "path:" + remoteFlakeDir(record.User) + "#default"
	}

	provider.ReportProgress(ctx, "reconciling environment (home-manager switch)")

	// --refresh forces Nix to re-fetch flake inputs rather than serve a
	// floating github: ref from its tarball cache (default TTL: 1
	// hour) -- without it, a just-pushed template fix wouldn't take
	// effect on an existing instance for up to an hour. --impure lets
	// common.nix read $USER/$HOME via builtins.getEnv to set
	// home.username/homeDirectory -- those vary per instance (see
	// state.Record.User), so they can't be hardcoded in the shared,
	// checked-in template.
	innerCmd := "nix run home-manager -- switch --no-write-lock-file --refresh --impure --flake " + shellcmd.Quote(flakeArg)
	cmd := shellcmd.LoginShell(innerCmd)
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

	// After the switch, not before: placing a credential on a box whose
	// environment failed to build helps nobody, and this is the path both
	// `up` and `provision` run -- so recovery after a reboot clears tmpfs is
	// `cloudlab provision`, which is already idempotent.
	placeDoltCredential(ctx, client, config.BeadsMode(cfg.Beads), filepath.Dir(cloudlabPath))
	return nil
}

// WriteAgentContext delivers cloudlab's session facts and the user's
// instructions files to every configured harness's global instruction
// file, and names the harnesses it cannot reach.
//
// Exported because session start needs the identical write: Reconcile runs
// on up and provision, while seedSession only borrows Connect for a
// client. instructions names files the user edits between sessions, so a
// session started after an edit would otherwise carry whatever the last up
// delivered, silently -- and that is the failure this feature exists to
// prevent. One function rather than two call sites each writing their own,
// so the two can never drift.
//
// agents comes from the caller because Reconcile has a resolved config in
// hand already and seedSession does not; resolving it twice here would cost
// a second pkl run on the path that already has the answer.
//
// An unreachable harness is a warning rather than an error in both
// callers. There is nothing to fix and nothing to retry -- the tool simply
// has no file for cross-project instructions -- so refusing to provision
// over it would only make a supported config unusable.
func WriteAgentContext(ctx context.Context, client *Client, user string, agents []string, cloudlabPath string) error {
	// Nobody to deliver to means nothing to read. agents is empty by
	// default, so this is the common case, and InstructionFiles is a pkl
	// run -- worth not paying on every up and every session start.
	if len(agents) == 0 {
		return nil
	}
	files, err := config.InstructionFiles(ctx, cloudlabPath)
	if err != nil {
		return err
	}
	unreachable, err := agentcontext.Write(
		agentcontext.Remote(client.Run, client.WriteFile, "/home/"+user),
		agents,
		files,
	)
	if err != nil {
		return err
	}
	for _, name := range unreachable {
		provider.ReportWarning(ctx, "instructions: "+name+" has no global instruction file, so cloudlab cannot deliver to it — put them in that tool's own settings")
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
