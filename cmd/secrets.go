package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/secrets"
	"github.com/spf13/cobra"
)

var (
	secretsStdinIsTerminal = isInteractive
	openInEditor           = defaultOpenInEditor
	secretKeyPattern       = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func validateSecretKey(key string) error {
	if key == "" || key == "sops" || !secretKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid secret key %q: keys must match ^[A-Za-z0-9_]+$ and cannot be %q", key, "sops")
	}
	return nil
}

func defaultOpenInEditor(ctx context.Context, _ *cobra.Command) ([]byte, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		if _, err := exec.LookPath("nano"); err == nil {
			editor = "nano"
		} else {
			editor = "vim"
		}
	}

	tmpDir, err := os.MkdirTemp("", "bivouac-secret-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpFile := filepath.Join(tmpDir, "secret")
	if err := os.WriteFile(tmpFile, nil, 0o600); err != nil {
		return nil, err
	}

	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return nil, errors.New("no editor configured")
	}

	// #nosec G204,G702 -- editor is user-configured $VISUAL or $EDITOR
	c := exec.CommandContext(ctx, parts[0], append(parts[1:], tmpFile)...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	resume := provider.SuspendTerminal(ctx)
	err = c.Run()
	_ = resume()
	if err != nil {
		return nil, fmt.Errorf("editor %q: %w", editor, err)
	}

	// #nosec G304 -- tmpFile is in a freshly created temp directory
	return os.ReadFile(tmpFile)
}

// newSecretsCmd builds the `bivouac secrets` command group. Unlike
// every lookupCommandSpecs entry, these never touch an instance or
// state.Record -- secrets.yaml is purely local to the developer's
// machine.
func newSecretsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "secrets",
		Short: "Manage bivouac's personal, sops-encrypted secrets file",
	}
	root.AddCommand(newSecretsInitCmd(), newSecretsEditCmd(), newSecretsKeysCmd(), newSecretsSetCmd())
	return root
}

func newSecretsInitCmd() *cobra.Command {
	var recipients []string
	c := &cobra.Command{
		Use:   "init",
		Short: "Create a fresh, empty secrets file encrypted for the given age recipient(s)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := secrets.Path()
			if err != nil {
				return err
			}
			if err := secrets.Init(cmd.Context(), path, recipients); err != nil {
				return err
			}
			cmd.Printf("Created %s\n", path)
			return nil
		},
	}
	c.Flags().StringArrayVar(&recipients, "age", nil, "age public key or YubiKey-plugin recipient to encrypt for (repeatable)")
	return c
}

func newSecretsEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the secrets file in $EDITOR via sops (decrypts, edits, re-encrypts)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := secrets.Path()
			if err != nil {
				return err
			}
			return secrets.Edit(cmd.Context(), path)
		},
	}
}

func newSecretsKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "List key names in the secrets file (never values)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := secrets.Path()
			if err != nil {
				return err
			}
			keys, err := secrets.Keys(path)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "no keys")
				return err
			}
			for _, k := range keys {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), k); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// newSecretsSetCmd builds the `bivouac secrets set` command, allowing
// callers to store string-scalar secrets from positional arguments, files,
// $EDITOR, piped stdin, or interactive prompts into secrets.yaml.
func newSecretsSetCmd() *cobra.Command {
	var (
		fileFlag   string
		editorFlag bool
		forceFlag  bool
		ageFlags   []string
	)
	c := &cobra.Command{
		Use:   "set <key> [value]",
		Short: "Store an encrypted secret in bivouac's personal secrets file",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := validateSecretKey(key); err != nil {
				return err
			}

			if len(args) > 1 && args[1] != "-" && len(args[1]) == 0 {
				return errors.New("secret value cannot be empty")
			}

			if fileFlag != "" && fileFlag != "-" {
				// #nosec G304 -- fileFlag is user-provided CLI path to read secret from
				st, err := os.Stat(fileFlag)
				if err != nil {
					return err
				}
				if st.IsDir() {
					return fmt.Errorf("%s is a directory", fileFlag)
				}
			}

			path, err := secrets.Path()
			if err != nil {
				return err
			}

			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					if len(ageFlags) > 0 {
						if err := secrets.Init(cmd.Context(), path, ageFlags); err != nil {
							return err
						}
					} else if secretsStdinIsTerminal() {
						cmd.Printf("Secrets file %s does not exist.\nEnter age recipient public key (e.g. age1...): ", path)
						line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
						if err != nil && !errors.Is(err, io.EOF) {
							return err
						}
						recipient := strings.TrimSpace(line)
						if recipient == "" {
							return errors.New("at least one --age recipient is required")
						}
						if err := secrets.Init(cmd.Context(), path, []string{recipient}); err != nil {
							return err
						}
					} else {
						return fmt.Errorf("secrets file %s doesn't exist yet (run `bivouac secrets init --age <key>` or pass --age)", path)
					}
				} else {
					return err
				}
			}

			keys, err := secrets.Keys(path)
			if err != nil {
				return err
			}
			exists := false
			for _, k := range keys {
				if k == key {
					exists = true
					break
				}
			}
			if exists {
				if forceFlag {
					// Proceed with overwrite
				} else if secretsStdinIsTerminal() {
					ok, err := confirm(cmd, fmt.Sprintf("Secret %q already exists in %s. Overwrite? [y/N]: ", key, path))
					if err != nil {
						return err
					}
					if !ok {
						cmd.Println("Aborted.")
						return nil
					}
				} else {
					return fmt.Errorf("secret %q already exists in %s (use --force to overwrite)", key, path)
				}
			}

			var val []byte
			switch {
			case len(args) > 1 && args[1] != "-":
				val = []byte(args[1])
			case fileFlag != "":
				if fileFlag == "-" {
					data, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					val = data
				} else {
					// #nosec G304 -- fileFlag is user-provided CLI path to read secret from
					data, err := os.ReadFile(fileFlag)
					if err != nil {
						return err
					}
					val = data
				}
			case editorFlag:
				data, err := openInEditor(cmd.Context(), cmd)
				if err != nil {
					return err
				}
				val = data
			case len(args) > 1 && args[1] == "-":
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				val = data
			case !secretsStdinIsTerminal():
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				val = data
			default:
				cmd.Printf("Paste secret for %q (Ctrl+D to submit, or press Enter to launch $EDITOR): ", key)
				reader := bufio.NewReader(cmd.InOrStdin())
				firstLine, err := reader.ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				if firstLine == "\n" || firstLine == "\r\n" {
					data, err := openInEditor(cmd.Context(), cmd)
					if err != nil {
						return err
					}
					val = data
				} else {
					rest, err := io.ReadAll(reader)
					if err != nil {
						return err
					}
					val = append([]byte(firstLine), rest...)
				}
			}

			if len(val) == 0 {
				return errors.New("secret value cannot be empty")
			}
			defer secrets.Zero(val)

			if err := secrets.Set(cmd.Context(), path, key, val); err != nil {
				return err
			}
			cmd.Printf("Set secret %q in %s\n", key, path)
			return nil
		},
	}
	c.Flags().StringVarP(&fileFlag, "file", "f", "", `read secret value from file (or "-" for standard input)`)
	c.Flags().BoolVarP(&editorFlag, "editor", "e", false, "open $EDITOR to compose or paste secret content")
	c.Flags().BoolVar(&forceFlag, "force", false, "overwrite existing secret without confirmation")
	c.Flags().StringArrayVar(&ageFlags, "age", nil, "age public key recipient(s) for auto-initialization if secrets file does not exist")
	return c
}
