package lifecycle

import (
	"context"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// InstanceStatus combines an instance's local state record with a live
// check of its current provider-side status.
type InstanceStatus struct {
	Record     state.Record
	LiveStatus string
	LiveErr    error
}

// Status reports record alongside a live provider.Get check. A Get
// failure (network error, VM destroyed outside cloudlab, etc.) is
// captured in LiveErr rather than failing the call -- local state is
// always more useful than nothing.
func Status(ctx context.Context, p provider.Provider, record state.Record) InstanceStatus {
	vm, err := p.Get(ctx, record.VMID)
	if err != nil {
		return InstanceStatus{Record: record, LiveErr: err}
	}
	return InstanceStatus{Record: record, LiveStatus: vm.Status}
}
