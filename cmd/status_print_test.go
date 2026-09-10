package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

func statusPrintCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	return c, &out
}

func TestPrintStatus_ShowsEveryRecordFieldAndTheCost(t *testing.T) {
	c, out := statusPrintCmd(t)
	st := lifecycle.InstanceStatus{
		Record: state.Record{
			Name: "myrepo", Provider: "digitalocean", Region: "blr1",
			Size: "s-2vcpu-4gb", Template: "python", User: "cloudlab",
			IP: "139.59.12.44", RepoPath: "~/sessions/myrepo",
		},
		LiveStatus: "active",
		Cost: lifecycle.Cost{
			Known: true, Uptime: 3*time.Hour + 12*time.Minute,
			Accrued: 0.42, Hourly: 0.03571, Monthly: 24,
		},
	}

	printStatus(c, st)

	got := out.String()
	for _, want := range []string{
		"myrepo", "active", "digitalocean", "blr1", "s-2vcpu-4gb",
		"python", "cloudlab", "139.59.12.44", "~/sessions/myrepo",
		"$0.42", "3h 12m", "$0.0357/hr",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printStatus() output does not mention %q:\n%s", want, got)
		}
	}
}

// The live check is the only source of both the status and the cost, so a
// failure has to take out both -- and say why, rather than showing a
// plausible-looking zero.
func TestPrintStatus_LiveFailureLeavesStatusAndCostUnknown(t *testing.T) {
	c, out := statusPrintCmd(t)
	st := lifecycle.InstanceStatus{
		Record:  state.Record{Name: "myrepo", IP: "139.59.12.44"},
		LiveErr: errors.New("no token found"),
	}

	printStatus(c, st)

	got := out.String()
	if strings.Count(got, "unknown") < 2 {
		t.Errorf("printStatus() = %q, want both status and cost reported as unknown", got)
	}
	if !strings.Contains(got, "no token found") {
		t.Errorf("printStatus() = %q, want the reason the live check failed", got)
	}
	// Local state is still worth showing when the provider is unreachable.
	if !strings.Contains(got, "139.59.12.44") {
		t.Errorf("printStatus() = %q, want the recorded IP to survive", got)
	}
}

// Records written before RepoPath existed have an empty one; that must
// read as an explained gap rather than a blank line.
func TestPrintStatus_NamesAnAbsentRepoPath(t *testing.T) {
	c, out := statusPrintCmd(t)

	printStatus(c, lifecycle.InstanceStatus{Record: state.Record{Name: "myrepo"}, LiveStatus: "active"})

	if !strings.Contains(out.String(), "unknown") {
		t.Errorf("printStatus() = %q, want an absent RepoPath called out", out.String())
	}
}
