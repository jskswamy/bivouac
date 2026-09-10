package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
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
