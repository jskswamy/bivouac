package lifecycle

import (
	"context"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/state"
)

// InstanceStatus combines an instance's local state record with a live
// check of its current provider-side status.
type InstanceStatus struct {
	Record     state.Record
	LiveStatus string
	// LiveSize is the instance's size as the provider reports it now.
	// The record's copy is only what bivouac asked for at creation and
	// nothing updates it, so a resize done outside bivouac leaves it
	// describing a machine that no longer exists. Empty when the live
	// check failed or the provider does not report a size, which callers
	// must read as "ask the record" rather than "no size".
	LiveSize string
	LiveErr  error
	// Cost is only ever populated from the live check: creation time and
	// price are the provider's facts, not ours. A failed check therefore
	// leaves Cost.Known false, which reads as "unknown" rather than free.
	Cost Cost
}

// Status reports record alongside a live provider.Get check. A Get
// failure (network error, VM destroyed outside bivouac, etc.) is
// captured in LiveErr rather than failing the call -- local state is
// always more useful than nothing.
func Status(ctx context.Context, p provider.Provider, record state.Record) InstanceStatus {
	return StatusAt(ctx, p, record, time.Now())
}

// StatusAt is Status with the clock passed in, so that cost -- which is
// a function of how long ago the instance was created -- can be tested
// without the answer changing between runs.
func StatusAt(ctx context.Context, p provider.Provider, record state.Record, now time.Time) InstanceStatus {
	vm, err := p.Get(ctx, record.VMID)
	if err != nil {
		return InstanceStatus{Record: record, LiveErr: err}
	}
	return InstanceStatus{
		Record:     record,
		LiveStatus: vm.Status,
		LiveSize:   vm.Size,
		Cost:       ComputeCost(vm, now),
	}
}
