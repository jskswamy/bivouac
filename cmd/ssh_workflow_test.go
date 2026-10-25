package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/keyplan"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
	"github.com/spf13/cobra"
)

type workflowProvider struct {
	fakeRegistry
	address    string
	createdVMs []provider.InstanceSpec
	destroyed  []string
}

func (p *workflowProvider) CreateKey(ctx context.Context, name, public string) (provider.SSHKey, error) {
	key, err := p.fakeRegistry.CreateKey(ctx, name, public)
	if err != nil {
		return key, err
	}
	key.ID = fmt.Sprintf("key-%d", len(p.created))
	p.created[len(p.created)-1] = key
	p.keys[len(p.keys)-1] = key
	return key, nil
}
func (p *workflowProvider) Create(_ context.Context, spec provider.InstanceSpec) (provider.VM, error) {
	p.createdVMs = append(p.createdVMs, spec)
	return provider.VM{ID: "vm-" + spec.Name, Name: spec.Name, IP: p.address, Region: spec.Region, Size: spec.Size}, nil
}
func (p *workflowProvider) Destroy(_ context.Context, id string) error {
	p.destroyed = append(p.destroyed, id)
	return nil
}
func (*workflowProvider) Get(context.Context, string) (provider.VM, error) {
	return provider.VM{}, provider.ErrNotFound
}
func (*workflowProvider) List(context.Context) ([]provider.VM, error) { return nil, nil }

func workflowExec(t *testing.T, ctx context.Context, record state.Record, operation string) {
	t.Helper()
	client, err := reconcile.Connect(ctx, record.IP, record.User)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	out, err := client.Run(operation)
	if err != nil || out != operation {
		t.Fatalf("%s: %v: %s", operation, err, out)
	}
}

func TestSSHWorkflow_SelectedKeyThroughCreationAndReuse(t *testing.T) {
	testenv.Isolate(t)
	f := newInitFixture(t)
	key := setupKey(t)
	p := &workflowProvider{}
	script := baseScript()
	script.keyChoice = []string{key.Path}
	if err := runInitFlowWith(context.Background(), f.out, script, f.root, setupSources(key, p), keySetupForUp); err != nil {
		t.Fatal(err)
	}
	if len(p.created) != 0 || len(p.createdVMs) != 0 {
		t.Fatal("implicit setup wrote to provider")
	}
	cfg, err := config.Resolve(context.Background(), f.project)
	if err != nil {
		t.Fatal(err)
	}
	var selected []sshkeys.Key
	for _, ref := range cfg.SSHKeyFiles {
		key, err := sshkeys.ReadPublicFile(ref)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, key)
	}
	keys, err := keyplan.Build(context.Background(), p, selected)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lifecycle.PrepareUp(context.Background(), "first", f.project, f.root, cfg, keys, image.Resolution{Requested: cfg.Image, Image: cfg.Image})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{key.Path, "will be registered", "managed SSH include", "bivouac-first"} {
		if !strings.Contains(upSummary(plan), want) {
			t.Fatalf("summary missing %q: %s", want, upSummary(plan))
		}
	}
	if len(p.created) != 0 || len(p.createdVMs) != 0 {
		t.Fatal("planning wrote to provider")
	}
	p.address = testenv.SSHServer(t, key)
	ready := 0
	provisioned := 0
	steps := lifecycle.Steps{
		WaitReady: func(ctx context.Context, ip, user string, _ time.Duration) error {
			client, err := reconcile.Connect(ctx, ip, user)
			if err != nil {
				return err
			}
			_ = client.Close()
			ready++
			return nil
		},
		Reconcile: func(ctx context.Context, name, _ string) error {
			store, err := state.Open()
			if err != nil {
				return err
			}
			record, ok, err := store.Get(name)
			if err != nil || !ok || record.SSH == nil {
				return fmt.Errorf("metadata not durable before provisioning: %v", err)
			}
			workflowExec(t, ctx, record, "provision")
			provisioned++
			return nil
		},
		JoinTailscale: func(context.Context, string, string) error { return nil },
	}
	// This call is the approval boundary, using the exact displayed plan.
	if err := lifecycle.Up(context.Background(), p, steps, plan); err != nil {
		t.Fatal(err)
	}
	if len(p.created) != 1 || len(p.createdVMs) != 1 || ready != 1 || provisioned != 1 {
		t.Fatalf("writes=%d VMs=%d ready=%d provisioned=%d", len(p.created), len(p.createdVMs), ready, provisioned)
	}
	if got := p.createdVMs[0].SSHKeys; len(got) != 1 || got[0] != "key-1" {
		t.Fatalf("authorized keys=%v", got)
	}
	command := &cobra.Command{}
	command.SetContext(context.Background())
	store, first, err := resolveInstance(command, "first")
	if err != nil {
		t.Fatal(err)
	}
	if first.SSH == nil || first.SSH.Keys[0].PublicPath != key.Path {
		t.Fatal("selected reference not persisted")
	}
	for _, name := range []string{"one", "two", "three"} {
		first.PutSession(state.Session{Name: name, LocalRepo: f.root})
		workflowExec(t, command.Context(), first, "session "+name)
	}
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	// A changed config selects a different key only for future creation.
	other := setupKey(t)
	f.write(t, f.basePath, fmt.Sprintf("region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys { %q }\n", other.Path))
	workflowExec(t, first.ConnectionContext(context.Background()), first, "transfer after config change")
	secondKeys, err := keyplan.Build(context.Background(), p, []sshkeys.Key{other})
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := lifecycle.PrepareUp(context.Background(), "second", f.project, f.root, cfg, secondKeys, image.Resolution{Requested: cfg.Image, Image: cfg.Image})
	if err != nil {
		t.Fatal(err)
	}
	p.address = testenv.SSHServer(t, other)
	if err := lifecycle.Up(context.Background(), p, steps, secondPlan); err != nil {
		t.Fatal(err)
	}
	_, second, err := resolveInstance(command, "second")
	if err != nil {
		t.Fatal(err)
	}
	workflowExec(t, command.Context(), second, "second transfer")
	if _, err := reconcile.Connect(command.Context(), first.IP, first.User); err == nil {
		t.Fatal("second record could authenticate to first endpoint")
	}
	if err := lifecycle.Down(first.ConnectionContext(context.Background()), p, store, first, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(first.Name); err != nil || ok {
		t.Fatalf("first state survived: %v", err)
	}
	workflowExec(t, second.ConnectionContext(context.Background()), second, "after first destroyed")
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(profiles.ConfigDir, "ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Host bivouac-first\n") || !strings.Contains(string(raw), "Host bivouac-second\n") {
		t.Fatalf("machine profile isolation: %s", raw)
	}
	if len(p.created) != 2 || len(p.destroyed) != 1 {
		t.Fatal("unexpected account or VM cleanup")
	}
}

func TestSSHWorkflow_LegacyConfigurationStopsAndCanBeRepaired(t *testing.T) {
	testenv.Isolate(t)
	f := newInitFixture(t)
	chdir(t, f.root)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys { \"12345\" }\n")
	f.write(t, f.project, "template = \"python\"\n")
	calls := 0
	command := newUpCmdWithProvider(func(context.Context) (provider.Provider, error) { calls++; return &workflowProvider{}, nil })
	command.SetContext(context.Background())
	command.SetArgs([]string{"fixture"})
	command.SetIn(strings.NewReader("n\n"))
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), f.basePath) || !strings.Contains(err.Error(), "bivouac init") || calls != 0 {
		t.Fatalf("migration error=%v provider calls=%d", err, calls)
	}
	key := setupKey(t)
	script := baseScript()
	script.keyChoice = []string{key.Path}
	script.decisions[promptPersonalAction] = personalUse
	if err := runInitFlowWith(context.Background(), f.out, script, f.root, setupSources(key, nil), keySetupForUp); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Resolve(context.Background(), f.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SSHKeyFiles) != 1 || cfg.SSHKeyFiles[0].Path != key.Path {
		t.Fatalf("migration result=%+v", cfg.SSHKeyFiles)
	}
}

func TestRunConnect_UnreachableTailscaleFallsBackToSSHForward(t *testing.T) {
	testenv.Isolate(t)
	key := testenv.SSHKey(t)
	ids, err := sshidentity.Select(context.Background(), []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}

	sshAddr := testenv.StreamSSHServer(t, key, func(command string) *exec.Cmd {
		if strings.Contains(command, "command -v tailscale") {
			return exec.Command("sh", "-c", "printf '%s\\n' /usr/bin/tailscale")
		}
		if strings.Contains(command, "ip -4") {
			return exec.Command("sh", "-c", "printf '%s\\n' 100.64.255.254")
		}
		if strings.Contains(command, "ss ") {
			return exec.Command("sh", "-c", "printf '%s\\n' 'LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:((\"python\",pid=1234,fd=3))'")
		}
		return exec.Command("true")
	})

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	c := sshidentity.Connection{
		Name:          "tsvm",
		Alias:         "bivouac-tsvm",
		User:          "me",
		PublicIP:      sshAddr,
		PreferredHost: sshAddr,
		Keys:          ids,
	}
	if err := store.Put(state.Record{
		Name:            "tsvm",
		Provider:        "digitalocean",
		IP:              sshAddr,
		User:            "me",
		TailscaleJoined: true,
		SSH:             &c,
	}); err != nil {
		t.Fatal(err)
	}

	// Stub ssh in PATH so lifecycle.Forward exits 0 immediately and records args.
	bin := t.TempDir()
	realSSH := "/usr/bin/ssh"
	if p, lookErr := filepath.EvalSymlinks("/usr/bin/ssh"); lookErr == nil {
		realSSH = p
	}
	sshScript := "#!/bin/sh\nif [ \"$1\" = -G ]; then exec " + realSSH + " \"$@\"; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(sshScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"connect", "--name", "tsvm", "--port", "8080", "--local-port", "18080"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("connect ExecuteContext err = %v\nout = %s", err, out.String())
	}
	if !strings.Contains(out.String(), "http://localhost:18080 (forwarding over SSH") {
		t.Fatalf("connect output = %q, want SSH forward to localhost:18080 when tailnet IP is unreachable locally", out.String())
	}
}
