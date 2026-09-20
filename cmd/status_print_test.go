package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/spf13/cobra"
)

// bufferedCmd is a command whose output is a buffer rather than a
// terminal, for the print and prompt helpers that take one.
func bufferedCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	return c, &out
}

func TestPrintStatus_ShowsEveryRecordFieldAndTheCost(t *testing.T) {
	c, out := bufferedCmd(t)
	st := lifecycle.InstanceStatus{
		Record: state.Record{
			Name: "myrepo", Provider: "digitalocean", Region: "blr1",
			Size: "s-2vcpu-4gb", Template: "python", User: "bivouac",
			IP: "139.59.12.44", RepoPath: "~/sessions/myrepo",
		},
		LiveStatus: "active",
		LiveSize:   "s-2vcpu-4gb",
		Cost: lifecycle.Cost{
			Known: true, Uptime: 3*time.Hour + 12*time.Minute,
			Accrued: 0.42, Hourly: 0.03571, Monthly: 24,
		},
	}

	printStatus(c, st)

	got := out.String()
	for _, want := range []string{
		"myrepo", "active", "digitalocean", "blr1", "s-2vcpu-4gb",
		"python", "bivouac", "139.59.12.44", "~/sessions/myrepo",
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
	c, out := bufferedCmd(t)
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
	c, out := bufferedCmd(t)

	printStatus(c, lifecycle.InstanceStatus{Record: state.Record{Name: "myrepo"}, LiveStatus: "active"})

	if !strings.Contains(out.String(), "unknown") {
		t.Errorf("printStatus() = %q, want an absent RepoPath called out", out.String())
	}
}

// The report has to agree with itself: cost comes off the live droplet,
// so the size printed beside it must too. A droplet resized outside
// bivouac leaves the record's slug behind at the new price.
func TestPrintStatus_PrefersTheLiveSizeOverTheRecordedOne(t *testing.T) {
	c, out := bufferedCmd(t)
	st := lifecycle.InstanceStatus{
		Record:     state.Record{Name: "myrepo", Size: "s-2vcpu-2gb", Template: "python"},
		LiveStatus: "active",
		LiveSize:   "s-2vcpu-4gb",
	}

	printStatus(c, st)

	got := out.String()
	if !strings.Contains(got, "s-2vcpu-4gb") {
		t.Errorf("printStatus() = %q, want the live size", got)
	}
	if strings.Contains(got, "s-2vcpu-2gb") {
		t.Errorf("printStatus() = %q, want the stale recorded size left out", got)
	}
}

// With no live read the recorded slug is all there is. It is still worth
// printing -- but as a record, not as the droplet's current size.
func TestPrintStatus_MarksTheRecordedSizeWhenNoLiveSizeIsAvailable(t *testing.T) {
	c, out := bufferedCmd(t)
	st := lifecycle.InstanceStatus{
		Record:  state.Record{Name: "myrepo", Size: "s-2vcpu-2gb", Template: "python"},
		LiveErr: errors.New("no token found"),
	}

	printStatus(c, st)

	got := out.String()
	if !strings.Contains(got, "s-2vcpu-2gb") {
		t.Errorf("printStatus() = %q, want the recorded size to survive", got)
	}
	if !strings.Contains(got, "recorded") {
		t.Errorf("printStatus() = %q, want the size marked as the recorded one", got)
	}
	// The pair line is given up in this case, so Template must not go
	// missing with it.
	if !strings.Contains(got, "python") {
		t.Errorf("printStatus() = %q, want the template still reported", got)
	}
}
