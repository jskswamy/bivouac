package cmd

import (
	"fmt"

	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all instances across all repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := state.Open()
			if err != nil {
				return err
			}
			records, err := store.List()
			if err != nil {
				return err
			}
			if len(records) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "no instances")
				return err
			}
			for _, r := range records {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", r.Name, r.Provider, r.IP); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
