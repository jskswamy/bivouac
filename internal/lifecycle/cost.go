package lifecycle

import (
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// Cost is what an instance has cost so far and the rate it is costing it
// at. Known is false when the provider could not tell us enough to say --
// callers must render that as "unknown" rather than as zero, because a
// running instance reported as free is worse than one reported as
// unknown.
type Cost struct {
	Uptime  time.Duration
	Accrued float64
	Hourly  float64
	Monthly float64
	Known   bool
}

// ComputeCost works out what vm has accrued by now.
//
// The anchor is the provider's creation timestamp rather than anything
// cloudlab records locally, because that is what the instance is actually
// billed from -- a droplet adopted or rebuilt outside `up` still bills
// from when it was created.
func ComputeCost(vm provider.VM, now time.Time) Cost {
	if vm.CreatedAt.IsZero() || vm.PriceHourly <= 0 {
		return Cost{Hourly: vm.PriceHourly, Monthly: vm.PriceMonthly}
	}

	uptime := now.Sub(vm.CreatedAt)
	// Clock skew between the API's created_at and this machine must not
	// turn into a negative bill.
	if uptime < 0 {
		uptime = 0
	}

	accrued := uptime.Hours() * vm.PriceHourly
	// DigitalOcean charges by the hour only up to the size's monthly
	// price. Without this cap a box left up for a couple of months would
	// be reported at several times what it actually costs.
	if vm.PriceMonthly > 0 && accrued > vm.PriceMonthly {
		accrued = vm.PriceMonthly
	}

	return Cost{
		Uptime:  uptime,
		Accrued: accrued,
		Hourly:  vm.PriceHourly,
		Monthly: vm.PriceMonthly,
		Known:   true,
	}
}
