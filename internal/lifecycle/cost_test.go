package lifecycle

import (
	"math"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// closeTo compares accrued money without demanding bit-exact float
// equality: an hourly rate like 0.05 is not representable, so
// 3 * 0.05 is 0.15000000000000002 rather than 0.15.
func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func TestComputeCost_AccruesFromCreationAtTheHourlyRate(t *testing.T) {
	now := time.Date(2026, 9, 8, 13, 30, 0, 0, time.UTC)
	vm := provider.VM{
		CreatedAt:    now.Add(-3 * time.Hour),
		PriceHourly:  0.05,
		PriceMonthly: 24,
	}

	cost := ComputeCost(vm, now)

	if !cost.Known {
		t.Fatal("cost.Known = false, want true")
	}
	if cost.Uptime != 3*time.Hour {
		t.Errorf("cost.Uptime = %v, want %v", cost.Uptime, 3*time.Hour)
	}
	if !closeTo(cost.Accrued, 0.15) {
		t.Errorf("cost.Accrued = %v, want %v", cost.Accrued, 0.15)
	}
}

// DigitalOcean bills hourly only up to the size's monthly price, so an
// instance left running for months must not report a number far above what
// is actually charged.
func TestComputeCost_CapsAccruedAtTheMonthlyPrice(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	vm := provider.VM{
		CreatedAt:    now.Add(-1000 * time.Hour),
		PriceHourly:  0.05,
		PriceMonthly: 24,
	}

	cost := ComputeCost(vm, now)

	if !closeTo(cost.Accrued, 24) {
		t.Errorf("cost.Accrued = %v, want the %v monthly cap", cost.Accrued, 24.0)
	}
}

func TestComputeCost_UnknownWithoutACreationTime(t *testing.T) {
	vm := provider.VM{PriceHourly: 0.05, PriceMonthly: 24}

	cost := ComputeCost(vm, time.Now())

	if cost.Known {
		t.Error("cost.Known = true, want false when creation time is unknown")
	}
}

// A zero price is "the provider did not tell us", never "this is free" --
// reporting $0.00 for a running instance would be a lie.
func TestComputeCost_UnknownWithoutAnHourlyPrice(t *testing.T) {
	vm := provider.VM{CreatedAt: time.Now().Add(-time.Hour)}

	cost := ComputeCost(vm, time.Now())

	if cost.Known {
		t.Error("cost.Known = true, want false when the hourly price is unknown")
	}
}

// Clock skew between the API's created_at and the local clock must not
// produce a negative uptime or a negative bill.
func TestComputeCost_ClampsAFutureCreationTimeToZero(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	vm := provider.VM{
		CreatedAt:    now.Add(time.Hour),
		PriceHourly:  0.05,
		PriceMonthly: 24,
	}

	cost := ComputeCost(vm, now)

	if cost.Uptime != 0 {
		t.Errorf("cost.Uptime = %v, want 0", cost.Uptime)
	}
	if cost.Accrued != 0 {
		t.Errorf("cost.Accrued = %v, want 0", cost.Accrued)
	}
}
