package lifecycle

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
)

// RouteStatus describes the active transport route to an instance and any
// fallback or unreachable overlay state.
type RouteStatus struct {
	Active             string `json:"active"`                       // "tailscale", "iap", or "public_ip"
	Address            string `json:"address,omitempty"`            // active IP when tailscale or public_ip
	Fallback           string `json:"fallback,omitempty"`           // "iap" or "public_ip" when active == "tailscale"
	TailscaleAddress   string `json:"tailscaleAddress,omitempty"`   // set when tailscale joined but unreachable
	TailscaleReachable *bool  `json:"tailscaleReachable,omitempty"` // set when tailscale joined
}

// Summary formats RouteStatus for the human-readable `bivouac status` report.
func (r RouteStatus) Summary() string {
	switch r.Active {
	case "tailscale":
		if r.Address != "" {
			return fmt.Sprintf("tailscale (%s)", r.Address)
		}
		return "tailscale"
	case "iap":
		if r.TailscaleAddress != "" {
			return fmt.Sprintf("iap tunnel (tailscale %s unreachable)", r.TailscaleAddress)
		}
		return "iap tunnel"
	case "public_ip":
		if r.TailscaleAddress != "" {
			if r.Address != "" {
				return fmt.Sprintf("public ip %s (tailscale %s unreachable)", r.Address, r.TailscaleAddress)
			}
			return fmt.Sprintf("public ip (tailscale %s unreachable)", r.TailscaleAddress)
		}
		if r.Address != "" {
			return fmt.Sprintf("public ip (%s)", r.Address)
		}
		return "public ip"
	default:
		return ""
	}
}

// ResolveRoute determines the active route and fallback state for record.
func ResolveRoute(record state.Record) RouteStatus {
	ctx := record.ConnectionContext(context.Background())
	c, hasConn := sshidentity.FromContext(ctx)

	base := "public_ip"
	publicIP := record.IP
	if hasConn && c.PublicIP != "" {
		publicIP = c.PublicIP
	}
	if (hasConn && c.UseIAP) || (record.Provider == "gcloud" && isPrivateIP(publicIP)) {
		base = "iap"
	}

	if hasConn && c.HasOverlay() {
		if c.OverlayReachable() {
			reachable := true
			return RouteStatus{
				Active:             "tailscale",
				Address:            c.PreferredHost,
				Fallback:           base,
				TailscaleReachable: &reachable,
			}
		}
		reachable := false
		st := RouteStatus{
			Active:             base,
			TailscaleAddress:   c.PreferredHost,
			TailscaleReachable: &reachable,
		}
		if base == "public_ip" {
			st.Address = publicIP
		}
		return st
	}

	if base == "iap" {
		return RouteStatus{Active: "iap"}
	}
	return RouteStatus{Active: "public_ip", Address: publicIP}
}

func isPrivateIP(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsPrivate()
}

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
	Cost  Cost
	Route RouteStatus
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
	route := ResolveRoute(record)
	vm, err := p.Get(ctx, record.VMID)
	if err != nil {
		return InstanceStatus{Record: record, LiveErr: err, Route: route}
	}
	return InstanceStatus{
		Record:     record,
		LiveStatus: vm.Status,
		LiveSize:   vm.Size,
		Cost:       ComputeCost(vm, now),
		Route:      route,
	}
}
