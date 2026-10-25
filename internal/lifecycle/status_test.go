package lifecycle

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
)

func TestStatus_ReportsLiveStatusOnSuccess(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "127.0.0.1"}
	p := &fakeProvider{getVM: provider.VM{ID: "vm-1", Status: "active"}}

	got := Status(context.Background(), p, record)

	if got.Record.Name != "myinstance" {
		t.Errorf("Record.Name = %q, want %q", got.Record.Name, "myinstance")
	}
	if got.LiveStatus != "active" {
		t.Errorf("LiveStatus = %q, want %q", got.LiveStatus, "active")
	}
	if got.LiveErr != nil {
		t.Errorf("LiveErr = %v, want nil", got.LiveErr)
	}
}

func TestStatus_RecordFieldsSurviveLiveCheckFailure(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "127.0.0.1"}
	p := &fakeProvider{getErr: errors.New("network error")}

	got := Status(context.Background(), p, record)

	if got.Record.Name != "myinstance" {
		t.Errorf("Record.Name = %q, want %q -- local fields must survive a live-check failure", got.Record.Name, "myinstance")
	}
	if got.LiveErr == nil {
		t.Error("LiveErr = nil, want the Get error")
	}
}

func TestStatus_ReportsCostFromTheLiveCheck(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1"}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	p := &fakeProvider{getVM: provider.VM{
		ID:           "vm-1",
		Status:       "active",
		CreatedAt:    now.Add(-2 * time.Hour),
		PriceHourly:  0.5,
		PriceMonthly: 24,
	}}

	got := StatusAt(context.Background(), p, record, now)

	if !got.Cost.Known {
		t.Fatal("Cost.Known = false, want true")
	}
	if got.Cost.Accrued != 1 {
		t.Errorf("Cost.Accrued = %v, want %v", got.Cost.Accrued, 1.0)
	}
	if got.Cost.Uptime != 2*time.Hour {
		t.Errorf("Cost.Uptime = %v, want %v", got.Cost.Uptime, 2*time.Hour)
	}
}

func TestStatus_CostUnknownWhenLiveCheckFails(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1"}
	p := &fakeProvider{getErr: errors.New("network error")}

	got := Status(context.Background(), p, record)

	if got.Cost.Known {
		t.Error("Cost.Known = true, want false when there is no live data to cost")
	}
}

// A droplet resized outside bivouac keeps its old slug in state, so the
// size the report prints has to come off the same live read that already
// answers status and cost.
func TestStatus_ReportsTheLiveSizeNotTheRecordedOne(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1", Size: "s-2vcpu-2gb"}
	p := &fakeProvider{getVM: provider.VM{ID: "vm-1", Status: "active", Size: "s-2vcpu-4gb"}}

	got := Status(context.Background(), p, record)

	if got.LiveSize != "s-2vcpu-4gb" {
		t.Errorf("LiveSize = %q, want %q", got.LiveSize, "s-2vcpu-4gb")
	}
	if got.Record.Size != "s-2vcpu-2gb" {
		t.Errorf("Record.Size = %q, want the record left untouched", got.Record.Size)
	}
}

func TestStatus_LiveSizeEmptyWhenLiveCheckFails(t *testing.T) {
	record := state.Record{Name: "myinstance", VMID: "vm-1", Size: "s-2vcpu-2gb"}
	p := &fakeProvider{getErr: errors.New("network error")}

	got := Status(context.Background(), p, record)

	if got.LiveSize != "" {
		t.Errorf("LiveSize = %q, want empty -- there is no live data to report", got.LiveSize)
	}
}

func TestResolveRoute_AllCombinations(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	reachableOverlay := ln.Addr().String()

	// 1. Tailscale joined and reachable locally (private GCE)
	r1 := ResolveRoute(state.Record{
		Name:            "inst1",
		Provider:        "gcloud",
		IP:              "10.160.0.9",
		TailscaleJoined: true,
		SSH: &sshidentity.Connection{
			Name:          "inst1",
			PublicIP:      "10.160.0.9",
			PreferredHost: reachableOverlay,
			UseIAP:        true,
		},
	})
	if r1.Active != "tailscale" || r1.Fallback != "iap" || r1.Summary() != "tailscale ("+reachableOverlay+")" {
		t.Fatalf("r1 = %+v, Summary() = %q", r1, r1.Summary())
	}

	// 2. Tailscale joined and unreachable locally on private GCE
	r2 := ResolveRoute(state.Record{
		Name:            "inst2",
		Provider:        "gcloud",
		IP:              "10.160.0.9",
		TailscaleJoined: true,
		SSH: &sshidentity.Connection{
			Name:          "inst2",
			PublicIP:      "10.160.0.9",
			PreferredHost: "127.0.0.1:1",
			UseIAP:        true,
		},
	})
	if r2.Active != "iap" || r2.TailscaleAddress != "127.0.0.1:1" || r2.Summary() != "iap tunnel (tailscale 127.0.0.1:1 unreachable)" {
		t.Fatalf("r2 = %+v, Summary() = %q", r2, r2.Summary())
	}

	// 3. Tailscale joined and unreachable locally on public IP
	r3 := ResolveRoute(state.Record{
		Name:            "inst3",
		Provider:        "digitalocean",
		IP:              "203.0.113.5",
		TailscaleJoined: true,
		SSH: &sshidentity.Connection{
			Name:          "inst3",
			PublicIP:      "203.0.113.5",
			PreferredHost: "127.0.0.1:1",
			UseIAP:        false,
		},
	})
	if r3.Active != "public_ip" || r3.Address != "203.0.113.5" || r3.Summary() != "public ip 203.0.113.5 (tailscale 127.0.0.1:1 unreachable)" {
		t.Fatalf("r3 = %+v, Summary() = %q", r3, r3.Summary())
	}

	// 4. Tailscale not joined on private GCE
	r4 := ResolveRoute(state.Record{
		Name:     "inst4",
		Provider: "gcloud",
		IP:       "10.160.0.9",
		SSH: &sshidentity.Connection{
			Name:          "inst4",
			PublicIP:      "10.160.0.9",
			PreferredHost: "10.160.0.9",
			UseIAP:        true,
		},
	})
	if r4.Active != "iap" || r4.Summary() != "iap tunnel" {
		t.Fatalf("r4 = %+v, Summary() = %q", r4, r4.Summary())
	}

	// 5. Tailscale not joined on public IP
	r5 := ResolveRoute(state.Record{
		Name:     "inst5",
		Provider: "digitalocean",
		IP:       "139.59.12.44",
	})
	if r5.Active != "public_ip" || r5.Address != "139.59.12.44" || r5.Summary() != "public ip (139.59.12.44)" {
		t.Fatalf("r5 = %+v, Summary() = %q", r5, r5.Summary())
	}
}
