package cmd

import (
	"fmt"
	"time"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	c := &cobra.Command{
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

			withCost, err := cmd.Flags().GetBool("cost")
			if err != nil {
				return err
			}
			var vms map[string]provider.VM
			if withCost {
				if vms, err = liveVMs(cmd); err != nil {
					return err
				}
			}

			return printInstances(cmd, records, vms, time.Now())
		},
	}
	// Off by default so the common `list` stays what it has always been:
	// local state, instant, and working on a machine with no API token.
	// Asking for cost is asking for the round trip, so unlike status --
	// which degrades because its live check is incidental -- a failure
	// here is a failure of the thing that was requested.
	c.Flags().Bool("cost", false, "look up live status and accrued cost from the provider")
	return c
}

// liveVMs fetches every VM on the account, keyed by the ID state records
// hold, so one API call covers every instance in the list.
func liveVMs(cmd *cobra.Command) (map[string]provider.VM, error) {
	ctx := cmd.Context()
	p, err := resolveProvider(ctx)
	if err != nil {
		return nil, err
	}
	live, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	vms := make(map[string]provider.VM, len(live))
	for _, vm := range live {
		vms[vm.ID] = vm
	}
	return vms, nil
}

// printInstances renders the instance table. A nil vms means no live
// lookup was made, and the status and cost columns are left out
// altogether rather than printed full of unknowns.
//
// Separate from the command so it can be tested without a provider or a
// token, and with a fixed clock -- accrued cost otherwise changes between
// runs. The write error is returned rather than dropped: `cloudlab list`
// piped into a closed pipe must fail rather than exit 0 having printed
// nothing.
func printInstances(cmd *cobra.Command, records []state.Record, vms map[string]provider.VM, now time.Time) error {
	out := cmd.OutOrStdout()
	s := newStyles(out)

	headers := []string{"NAME", "PROVIDER", "IP"}
	if vms != nil {
		headers = append(headers, "STATUS", "COST")
	}

	rows := make([][]string, 0, len(records))
	var total float64
	for _, r := range records {
		row := []string{s.value.Render(r.Name), r.Provider, r.IP}
		if vms != nil {
			// A record with no matching droplet is one destroyed outside
			// cloudlab, or stale state. Both are worth showing as a row --
			// that gap is the useful part -- but neither can be costed.
			vm, ok := vms[r.VMID]
			cost := "?"
			status := "unknown"
			if ok {
				status = vm.Status
				if c := lifecycle.ComputeCost(vm, now); c.Known {
					cost = formatMoney(c.Accrued)
					total += c.Accrued
				}
			}
			row = append(row, s.statusDot(status), cost)
		}
		rows = append(rows, row)
	}

	var footer []string
	if vms != nil {
		footer = make([]string, len(headers))
		footer[len(footer)-1] = formatMoney(total)
	}

	_, err := fmt.Fprintf(out, "\n%s", renderTable(s, headers, rows, footer))
	return err
}
