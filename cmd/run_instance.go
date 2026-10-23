package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean"
	"github.com/jskswamy/bivouac/internal/provider/gcloud"
	"github.com/jskswamy/bivouac/internal/secrets"
	"github.com/jskswamy/bivouac/internal/state"
)

// resolveInstance opens the state store, looks up name, and returns
// the same "no instance named" error every command below list/up
// reports identically when the name doesn't resolve to a known
// instance.
func resolveInstance(cmd *cobra.Command, name string) (*state.Store, state.Record, error) {
	store, err := state.Open()
	if err != nil {
		return nil, state.Record{}, err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return nil, state.Record{}, err
	}
	if !ok {
		return nil, state.Record{}, fmt.Errorf("no instance named %q (run bivouac up first)", name)
	}
	cmd.SetContext(record.ConnectionContext(cmd.Context()))
	return store, record, nil
}

// resolveProvider builds a DigitalOcean provider from the token
// resolveToken finds, for backward compatibility or when provider is digitalocean.
func resolveProvider(ctx context.Context) (provider.Provider, error) {
	return resolveProviderFor(ctx, "digitalocean", "", "")
}

// resolveProviderForRecord returns the appropriate provider for an existing instance record.
func resolveProviderForRecord(ctx context.Context, record state.Record) (provider.Provider, error) {
	prov := record.Provider
	if prov == "" {
		prov = "digitalocean"
	}
	return resolveProviderFor(ctx, prov, record.Project, record.Region)
}

// resolveProviderForConfig returns the appropriate provider for an instance being created from config.
func resolveProviderForConfig(ctx context.Context, cfg config.Resolved) (provider.Provider, error) {
	prov := cfg.Provider
	if prov == "" {
		prov = "digitalocean"
	}
	project := ""
	var opts []gcloud.Option
	if cfg.GCloud != nil {
		project = cfg.GCloud.Project
		if cfg.GCloud.DiskSizeGb > 0 {
			opts = append(opts, gcloud.WithDiskSizeGb(cfg.GCloud.DiskSizeGb))
		}
		if cfg.GCloud.ExternalIP {
			opts = append(opts, gcloud.WithExternalIP(true))
		}
	}
	return resolveProviderFor(ctx, prov, project, cfg.Region, opts...)
}

type dispatchProviderResolver func(ctx context.Context, providerName, project, region string) (provider.Provider, error)

var customProviderResolver dispatchProviderResolver

func resolveGCloudOptions(ctx context.Context) ([]gcloud.Option, error) {
	var opts []gcloud.Option
	if raw := os.Getenv("GCLOUD_API_URL"); raw != "" {
		opts = append(opts, gcloud.WithBaseURL(raw))
		return opts, nil
	}

	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		opts = append(opts, gcloud.WithCredentialsFile(path))
		return opts, nil
	}

	if keyVal := os.Getenv("GCLOUD_SERVICE_ACCOUNT_KEY"); keyVal != "" {
		if strings.HasPrefix(strings.TrimSpace(keyVal), "{") {
			opts = append(opts, gcloud.WithCredentialsJSON([]byte(keyVal)))
		} else {
			opts = append(opts, gcloud.WithCredentialsFile(keyVal))
		}
		return opts, nil
	}

	secPath, err := secrets.Path()
	if err == nil {
		value, err := secrets.Decrypt(ctx, secPath, "gcloud_service_account_key")
		if err == nil && len(value) > 0 {
			opts = append(opts, gcloud.WithCredentialsJSON(value))
			return opts, nil
		}
	}

	return opts, nil
}

var newGCloud = func(ctx context.Context, project, region string, extraOpts ...gcloud.Option) (provider.Provider, error) {
	opts, err := resolveGCloudOptions(ctx)
	if err != nil {
		return nil, err
	}
	opts = append(opts, extraOpts...)
	return gcloud.New(project, region, opts...)
}

// resolveProviderFor dispatches to the requested cloud provider.
func resolveProviderFor(ctx context.Context, providerName, project, region string, extraOpts ...gcloud.Option) (provider.Provider, error) {
	if customProviderResolver != nil {
		return customProviderResolver(ctx, providerName, project, region)
	}
	switch providerName {
	case "digitalocean":
		token, err := resolveToken(ctx)
		if err != nil {
			return nil, err
		}
		p, err := newDigitalOcean(token)
		if err != nil {
			return nil, err
		}
		return p, nil
	case "gcloud":
		return newGCloud(ctx, project, region, extraOpts...)
	default:
		return nil, fmt.Errorf("unsupported provider %q", providerName)
	}
}

// apiURLEnv points every DigitalOcean client bivouac builds at another
// API. The name is the one the Packer DigitalOcean plugin reads, so one
// variable sends both bivouac and the image build's Packer to the same
// place -- which is how the end-to-end harness runs the real binary and
// the real plugin against a fake account.
const apiURLEnv = "DIGITALOCEAN_API_URL"

// newDigitalOcean is the one place production builds a DigitalOcean
// provider, so that the API override reaches every command that talks
// to the account, not only the ones that happen to go through
// resolveProvider. A provider that silently ignored it would send a
// test's dummy token to the real API.
func newDigitalOcean(token string) (*digitalocean.Provider, error) {
	raw := os.Getenv(apiURLEnv)
	if raw == "" {
		return digitalocean.New(token)
	}
	p, err := digitalocean.New(token, digitalocean.WithBaseURL(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", apiURLEnv, err)
	}
	apiNotice.say("Using DigitalOcean API at " + raw + "\n")
	return p, nil
}

// onceNotice prints its first message and drops the rest. The override
// is announced because an API that is not DigitalOcean's must never be
// in use silently; once is enough, since one run can build more than
// one provider (up's SSH-key question builds its own before up does).
type onceNotice struct {
	once sync.Once
	w    io.Writer
}

func (n *onceNotice) say(msg string) {
	n.once.Do(func() { _, _ = io.WriteString(n.w, msg) })
}

// apiNotice goes to stderr so it never mixes into --json output.
var apiNotice = &onceNotice{w: os.Stderr}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(cmd, name)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	p, err := resolveProviderForRecord(ctx, record)
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
	if record.Project != "" {
		fmt.Fprintf(&b, "  Project:  %s\n", record.Project)
	}
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
	_, record, err := resolveInstance(cmd, name)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if !jsonFlag(cmd) {
		ctx = progressCtx(cmd)
	}

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
	if p, provErr := resolveProviderForRecord(ctx, record); provErr != nil {
		st.LiveErr = provErr
	} else {
		st = lifecycle.Status(ctx, p, record)
	}
	if jsonFlag(cmd) {
		return printStatusJSON(cmd, st, record)
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
	store, record, err := resolveInstance(cmd, name)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	if err := lifecycle.JoinTailscale(ctx, record.IP, record.User); err != nil {
		return err
	}
	if err := store.Update(record.Name, func(latest *state.Record) error {
		latest.TailscaleJoined = true
		return nil
	}); err != nil {
		return err
	}
	if err := lifecycle.RefreshTailnetRoute(ctx); err != nil {
		return err
	}
	cmd.Printf("%s joined the tailnet\n", name)
	return nil
}
