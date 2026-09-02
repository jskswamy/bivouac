package lifecycle

import (
	"context"
	"errors"
	"testing"

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
