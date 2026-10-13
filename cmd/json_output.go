package cmd

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func jsonFlag(cmd *cobra.Command) bool {
	jsonOut, _ := cmd.Flags().GetBool("json")
	return jsonOut
}
