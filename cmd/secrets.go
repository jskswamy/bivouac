package cmd

import (
	"fmt"

	"github.com/jskswamy/cloudlab/internal/secrets"
	"github.com/spf13/cobra"
)

// newSecretsCmd builds the `cloudlab secrets` command group. Unlike
// every lookupCommandSpecs entry, these never touch an instance or
// state.Record -- secrets.yaml is purely local to the developer's
// machine.
func newSecretsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "secrets",
		Short: "Manage cloudlab's personal, sops-encrypted secrets file",
	}
	root.AddCommand(newSecretsInitCmd(), newSecretsEditCmd(), newSecretsKeysCmd())
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
