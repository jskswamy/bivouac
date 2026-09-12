package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
	"github.com/jskswamy/cloudlab/internal/state"
)

// resolveInstance opens the state store, looks up name, and returns
// the same "no instance named" error every command below list/up
// reports identically when the name doesn't resolve to a known
// instance.
func resolveInstance(name string) (*state.Store, state.Record, error) {
	store, err := state.Open()
	if err != nil {
		return nil, state.Record{}, err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return nil, state.Record{}, err
	}
	if !ok {
		return nil, state.Record{}, fmt.Errorf("no instance named %q (run cloudlab up first)", name)
	}
	return store, record, nil
}

// resolveProvider builds a DigitalOcean provider from the token
// resolveToken finds, for the three commands that call the live API
// (up, down, status). Every other command works off state.Record and
// SSH and needs no token at all.
func resolveProvider(ctx context.Context) (provider.Provider, error) {
	token, err := resolveToken(ctx)
	if err != nil {
		return nil, err
	}
	return digitalocean.New(token), nil
}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	p, err := resolveProvider(ctx)
	if err != nil {
		return err
	}

	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}

	ok, err := confirm(cmd, downSummary(record))
	if err != nil {
		return err
	}
	if !ok {
		cmd.Println("Aborted.")
		return nil
	}

	if err := lifecycle.Down(cmd.Context(), p, store, record, force); err != nil {
		return err
	}
	cmd.Printf("Instance %s is down\n", name)
	return nil
}

// downSummary describes the instance down is about to destroy, and
// warns the destruction is unrecoverable, for confirmation before
// anything irreversible happens.
func downSummary(record state.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This will destroy instance %q -- this cannot be undone:\n", record.Name)
	fmt.Fprintf(&b, "  Provider: %s\n", record.Provider)
	fmt.Fprintf(&b, "  Region:   %s\n", record.Region)
	fmt.Fprintf(&b, "  Size:     %s\n", record.Size)
	fmt.Fprintf(&b, "  Template: %s\n", record.Template)
	fmt.Fprintf(&b, "  IP:       %s\n", record.IP)
	b.WriteString("Any unsaved work on the instance will be lost.\n")
	b.WriteString("Proceed? [y/N]: ")
	return b.String()
}

// servingLookupTimeout bounds what status is willing to wait for the
// instance's published ports.
//
// Generous enough for a loaded instance -- the box this was found on was
// swapping under three concurrent sessions and still answered other
// commands in single-digit seconds -- and short enough that a report stays
// a report. Anything slower is indistinguishable from unreachable, and
// status says so rather than waiting to find out.
const servingLookupTimeout = 20 * time.Second

func runStatus(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)

	// status uses the API for the live status field and for cost, which
	// is derived from the droplet's creation time and price; every other
	// line it prints comes from local state. lifecycle.Status already
	// reports a failed live check rather than failing the call -- an
	// absent token is the same kind of fact, so it degrades the same way
	// rather than making the whole report unavailable on a machine with
	// no key present. up and down still fail hard: neither can create or
	// destroy a droplet without the API, so for them a missing token is
	// not a degraded report but no work at all.
	st := lifecycle.InstanceStatus{Record: record}
	if p, provErr := resolveProvider(ctx); provErr != nil {
		st.LiveErr = provErr
	} else {
		st = lifecycle.Status(ctx, p, record)
	}
	printStatus(cmd, st)
	printSessions(cmd, record)

	// Only when state says there is a tailnet: serving is tailnet-only,
	// so an instance that never joined has nothing to report and should
	// not pay an SSH round trip to learn that.
	if record.TailscaleJoined {
		// Bounded because status is a report. An instance stops answering
		// for reasons this side cannot tell apart -- it ran out of memory,
		// a daemon wedged, the tailnet went down, the connection went
		// half-open -- and status should survive all of them rather than
		// try to diagnose any. Without this the report printed everything
		// up to Serving and then hung, which is worse than the "unknown"
		// it was already written to fall back to.
		//
		// Derived from ctx rather than cmd.Context(): ctx carries the
		// progress reporter built above, and starting again from the bare
		// command context would silently drop it.
		serveCtx, cancel := context.WithTimeout(ctx, servingLookupTimeout)
		defer cancel()

		// Said before the wait, not after. This is the one step in status
		// that can take twenty seconds, and an unexplained pause after the
		// last printed line is what a hang looks like -- the report should
		// not have to be timed to tell the difference.
		provider.ReportProgress(ctx, "checking published ports")

		entries, serveErr := lifecycle.ServeStatus(serveCtx, record.IP, record.User)
		// The tailnet IP is only needed to print an address beside each
		// entry, so it is fetched only when there is an entry to print --
		// skipping a second SSH round trip both when the instance is
		// unreachable (serveErr already answers that) and in the common
		// case of nothing being served.
		var tailnetIP string
		if serveErr == nil && len(entries) > 0 {
			tailnetIP, _ = lifecycle.TailscaleIP(serveCtx, record.IP, record.User)
		}
		printServing(cmd, entries, tailnetIP, serveErr)
	}
	return nil
}

func runTailscale(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	if err := lifecycle.JoinTailscale(ctx, record.IP, record.User); err != nil {
		return err
	}
	record.TailscaleJoined = true
	if err := store.Put(record); err != nil {
		return err
	}
	cmd.Printf("%s joined the tailnet\n", name)
	return nil
}
