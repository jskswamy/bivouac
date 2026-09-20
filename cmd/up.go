package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/lifecycle"
)

func newUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [name]",
		Short: "Create the VM, provision it, and bring the instance fully live",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, name, err := instanceIdentity(cmd, args)
			if err != nil {
				return err
			}

			bivouacPath := filepath.Join(root, "bivouac.pkl")
			cfg, err := resolveOrInit(cmd, bivouacPath, root)
			if err != nil {
				return err
			}
			cfg, err = ensureSSHKeys(cmd, cfg, bivouacPath)
			if err != nil {
				return err
			}

			ctx := progressCtx(cmd)
			// Resolved before the confirmation prompt, as the inline env
			// read this replaces was: there is no point asking whether to
			// create an instance that cannot be created. The cost is that a
			// secrets-file token is decrypted even when the answer turns out
			// to be no -- the cheaper order of the two, since up is confirmed
			// far more often than it is aborted.
			p, err := resolveProvider(ctx)
			if err != nil {
				return fmt.Errorf("%w (needed to create instance %q)", err, name)
			}

			ok, err := confirm(cmd, upSummary(name, bivouacPath, cfg))
			if err != nil {
				return err
			}
			if !ok {
				cmd.Println("Aborted.")
				return nil
			}

			if err := lifecycle.Up(ctx, p, lifecycle.DefaultSteps(), name, bivouacPath, root); err != nil {
				return err
			}
			cmd.Printf("Instance %s is up\n", name)
			return nil
		},
	}
	return cmd
}

// resolveOrInit loads the project's config, falling back to the
// interactive flow when the repository has none yet and then loading
// what that wrote.
//
// This does not contradict the earlier decision that bivouac.pkl must
// exist with no implicit fallback: the file still exists before
// Reconcile runs. Creating it by asking is only friendlier than
// refusing -- and refusing is still what happens with no terminal to
// ask on, because a form blocked on stdin that never arrives looks
// exactly like a hang.
func resolveOrInit(cmd *cobra.Command, bivouacPath, root string) (config.Resolved, error) {
	cfg, err := config.Resolve(cmd.Context(), bivouacPath)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if terr := requireTerminal(isInteractive()); terr != nil {
		return config.Resolved{}, fmt.Errorf("no bivouac.pkl in %s: %w", root, terr)
	}
	cmd.Printf("No bivouac.pkl in %s yet — let's create one.\n", root)
	if err := runInitFlow(cmd.Context(), cmd.OutOrStdout(), newFormPrompter(), root); err != nil {
		return config.Resolved{}, err
	}
	return config.Resolve(cmd.Context(), bivouacPath)
}

// ensureSSHKeys asks for and records at least one SSH key when cfg has
// none, reusing the exact discovery/upload flow `bivouac init` offers
// for the same question -- scoped to just this one field, since up has
// no business re-litigating region, size or template just because a key
// is missing.
//
// sshKeys is optional in the schema (an empty account has none to name
// yet), so Resolve does not, and cannot, catch this. Left unchecked, up
// silently creates a droplet with no key registered: DigitalOcean boots
// it, bills it, and neither password nor public-key auth has anything
// to offer, which is exactly the incident that motivated this guard.
//
// A user who is asked and still ends with none has that choice
// respected everywhere else in this codebase (init only warns) -- but
// up is the one place that spends money on the strength of the answer,
// so here it refuses rather than repeating the mistake it exists to
// prevent.
func ensureSSHKeys(cmd *cobra.Command, cfg config.Resolved, bivouacPath string) (config.Resolved, error) {
	if cfg.SshKeys != nil && len(*cfg.SshKeys) > 0 {
		return cfg, nil
	}
	if err := requireTerminal(isInteractive()); err != nil {
		return config.Resolved{}, fmt.Errorf("no SSH keys configured, and up refuses to create an instance nothing can log into: %w", err)
	}
	return ensureSSHKeysWith(cmd, bivouacPath, newFormPrompter(), defaultKeySources())
}

// ensureSSHKeysWith asks for and records at least one SSH key, reusing
// the same discovery/upload flow `bivouac init` offers for the same
// question. Split from ensureSSHKeys, which owns the "already has keys"
// and "no terminal" checks, so this half -- the asking, writing and
// refuse-on-empty logic -- is testable without a terminal, an ssh-agent
// or a network, the same split runInitFlow/runInitFlowWith uses.
func ensureSSHKeysWith(cmd *cobra.Command, bivouacPath string, p prompter, keys keySources) (config.Resolved, error) {
	basePath, err := config.DefaultBasePath()
	if err != nil {
		return config.Resolved{}, err
	}
	existing, _, err := readIfPresent(cmd.Context(), basePath)
	if err != nil {
		return config.Resolved{}, err
	}

	cmd.Printf("%s has no sshKeys configured — up will create an instance nothing can log into without one.\n", bivouacPath)
	chosen, err := askSSHKeys(cmd.Context(), cmd.OutOrStdout(), p, keys, currentKeys(existing))
	if err != nil {
		return config.Resolved{}, err
	}
	if len(chosen) == 0 {
		return config.Resolved{}, errors.New("no SSH keys selected; refusing to create an instance nothing can log into")
	}

	existing[config.FieldSSHKeys] = chosen
	// 0o600: base.pkl records personal choices and is not shared.
	if err := writeValues(basePath, existing, 0o600); err != nil {
		return config.Resolved{}, err
	}
	cmd.Printf("Wrote %s\n", basePath)

	return config.Resolve(cmd.Context(), bivouacPath)
}

// upSummary describes the instance up is about to create, for
// confirmation before anything billable happens.
//
// The config path is named, not just the values read from it, because
// the file being read is not always the one the user is standing next
// to. An instance is identified by its repository, so RepoRoot resolves
// through .git to the main checkout -- run from a linked worktree, up
// reads the main checkout's bivouac.pkl and ignores one sitting beside
// the caller. That is the consistent behaviour (one instance, one
// config) but it is invisible, and it cost a live-fire run that died at
// a sops step the worktree's config had switched off. Printing the path
// makes the surprise cost one glance instead of a debugging session.
func upSummary(name, bivouacPath string, cfg config.Resolved) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This will create instance %q:\n", name)
	fmt.Fprintf(&b, "  Config:   %s\n", bivouacPath)
	fmt.Fprintf(&b, "  Region:   %s\n", cfg.Region)
	fmt.Fprintf(&b, "  Size:     %s\n", cfg.Size)
	fmt.Fprintf(&b, "  Template: %s\n", cfg.Template)
	if cfg.Image != "" {
		fmt.Fprintf(&b, "  Image:    %s\n", cfg.Image)
	}
	b.WriteString("Proceed? [y/N]: ")
	return b.String()
}
