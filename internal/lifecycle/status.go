package lifecycle

import (
	"context"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// InstanceStatus combines an instance's local state record with a live
// check of its current provider-side status.
type InstanceStatus struct {
	Record     state.Record
	LiveStatus string
	LiveErr    error
	// Cost is only ever populated from the live check: creation time and
	// price are the provider's facts, not ours. A failed check therefore
	// leaves Cost.Known false, which reads as "unknown" rather than free.
	Cost Cost
}

// Status reports record alongside a live provider.Get check. A Get
// failure (network error, VM destroyed outside cloudlab, etc.) is
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
		Cost:       ComputeCost(vm, now),
	}
}
