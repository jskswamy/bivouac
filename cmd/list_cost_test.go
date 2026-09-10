package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

func listTestCmd() (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	return c, &out
}

var listNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// Without --cost, list stays what it always was: local state only, and no
// column that would need a live lookup to fill in.
func TestPrintInstances_WithoutCostShowsNoLiveColumns(t *testing.T) {
	c, out := listTestCmd()
	records := []state.Record{{Name: "myrepo", Provider: "digitalocean", IP: "1.2.3.4", VMID: "1"}}

	_ = printInstances(c, records, nil, listNow)

	got := out.String()
	for _, want := range []string{"NAME", "myrepo", "digitalocean", "1.2.3.4"} {
		if !strings.Contains(got, want) {
			t.Errorf("printInstances() = %q, want it to contain %q", got, want)
		}
	}
	for _, unwanted := range []string{"COST", "STATUS"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("printInstances() = %q, want no %q column without --cost", got, unwanted)
		}
	}
}

func TestPrintInstances_WithCostShowsEachInstanceAndTheTotal(t *testing.T) {
	c, out := listTestCmd()
	records := []state.Record{
		{Name: "myrepo", Provider: "digitalocean", IP: "1.2.3.4", VMID: "1"},
		{Name: "other", Provider: "digitalocean", IP: "5.6.7.8", VMID: "2"},
	}
	vms := map[string]provider.VM{
		"1": {Status: "active", CreatedAt: listNow.Add(-10 * time.Hour), PriceHourly: 0.01, PriceMonthly: 7},
		"2": {Status: "off", CreatedAt: listNow.Add(-20 * time.Hour), PriceHourly: 0.01, PriceMonthly: 7},
	}

	_ = printInstances(c, records, vms, listNow)

	got := out.String()
	for _, want := range []string{"COST", "STATUS", "active", "off", "$0.10", "$0.20", "$0.30"} {
		if !strings.Contains(got, want) {
			t.Errorf("printInstances() = %q, want it to contain %q", got, want)
		}
	}
}

// A droplet that state knows about but the API did not return (destroyed
// outside cloudlab, or a stale record) must read as unknown, and must not
// be silently counted as $0.00 in the total.
func TestPrintInstances_UnknownCostIsNotCountedInTheTotal(t *testing.T) {
	c, out := listTestCmd()
	records := []state.Record{
		{Name: "myrepo", Provider: "digitalocean", VMID: "1"},
		{Name: "gone", Provider: "digitalocean", VMID: "99"},
	}
	vms := map[string]provider.VM{
		"1": {Status: "active", CreatedAt: listNow.Add(-10 * time.Hour), PriceHourly: 0.01, PriceMonthly: 7},
	}

	_ = printInstances(c, records, vms, listNow)

	got := out.String()
	if !strings.Contains(got, "?") {
		t.Errorf("printInstances() = %q, want an unknown cost marked", got)
	}
	if !strings.Contains(got, "$0.10") {
		t.Errorf("printInstances() = %q, want the total to be just the known instance", got)
	}
}

func TestListCommand_HasACostFlag(t *testing.T) {
	if newListCmd().Flags().Lookup("cost") == nil {
		t.Error("list has no --cost flag")
	}
}
