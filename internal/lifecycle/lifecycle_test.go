package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// fakeProvider is a minimal provider.Provider test double: Create
// returns a canned VM (or err, if set); the other three methods are
// unused by Up but must exist to satisfy the interface.
type fakeProvider struct {
	vm          provider.VM
	err         error
	created     bool
	gotSpec     provider.InstanceSpec
	destroyedID string
	destroyed   bool
	destroyErr  error
	getVM       provider.VM
	getErr      error
}

func (f *fakeProvider) Create(ctx context.Context, spec provider.InstanceSpec) (provider.VM, error) {
	f.created = true
	f.gotSpec = spec
	return f.vm, f.err
}
func (f *fakeProvider) Destroy(ctx context.Context, id string) error {
	f.destroyedID = id
	f.destroyed = true
	return f.destroyErr
}
func (f *fakeProvider) Get(ctx context.Context, id string) (provider.VM, error) {
	return f.getVM, f.getErr
}
func (f *fakeProvider) List(ctx context.Context) ([]provider.VM, error) { return nil, nil }

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

func minimalCloudlabPkl(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, path, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")
	return path
}

func TestUp_RunsFullSequenceInOrder(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "", 0
	})

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: addr, Region: "nyc3", Size: "s-1vcpu-1gb"}}

	wantUser, err := identity.RemoteUser()
	if err != nil {
		t.Fatalf("identity.RemoteUser() error = %v", err)
	}
	wantRemotePath, err := RemotePath(repoRoot, wantUser)
	if err != nil {
		t.Fatalf("RemotePath() error = %v", err)
	}

	steps := Steps{
		WaitReady: WaitReady,
		Reconcile: reconcile.Reconcile,
	}

	var progress []string
	ctx := provider.WithProgress(context.Background(), func(status string) { progress = append(progress, status) })

	if err := Up(ctx, p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if len(progress) == 0 || !strings.Contains(progress[0], "creating") {
		t.Errorf("progress = %v, want a first entry mentioning creating the instance", progress)
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("myinstance")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("state.Get(myinstance) = not found, want recorded")
	}
	if record.VMID != "vm-1" {
		t.Errorf("record.VMID = %q, want %q", record.VMID, "vm-1")
	}
	if record.User != wantUser {
		t.Errorf("record.User = %q, want %q", record.User, wantUser)
	}
	if record.RepoPath != wantRemotePath {
		t.Errorf("record.RepoPath = %q, want %q", record.RepoPath, wantRemotePath)
	}

	if p.gotSpec.Name != "myinstance" {
		t.Errorf("InstanceSpec.Name = %q, want %q", p.gotSpec.Name, "myinstance")
	}
	if p.gotSpec.Image != "ubuntu-24-04-x64" {
		t.Errorf("InstanceSpec.Image = %q, want %q", p.gotSpec.Image, "ubuntu-24-04-x64")
	}
	if p.gotSpec.Region != "nyc3" {
		t.Errorf("InstanceSpec.Region = %q, want %q", p.gotSpec.Region, "nyc3")
	}
	if p.gotSpec.Size != "s-1vcpu-1gb" {
		t.Errorf("InstanceSpec.Size = %q, want %q", p.gotSpec.Size, "s-1vcpu-1gb")
	}
}

func TestUp_StateNotRecordedWhenCreateFails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{err: errors.New("create failed")}

	err := Up(context.Background(), p, DefaultSteps(), "myinstance", cloudlabPath, repoRoot)
	if err == nil {
		t.Fatal("Up() error = nil, want error when Create fails")
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := store.Get("myinstance")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("state.Get(myinstance) = found, want not recorded since Create failed")
	}
}

// Re-running up against an instance that already exists must not create a
// second droplet.
//
// Up went straight to Create with no store lookup, then overwrote the record
// with the new IP. The first droplet kept running and became unreachable
// through the CLI entirely -- list did not show it, down could not find it.
// Observed live: a provisioning failure, one re-run, and a droplet that had to
// be deleted through the provider API by hand. The cost is not the money; a
// session on the orphan is unreachable work, which contradicts the whole
// rescue-before-destroy guarantee.
func TestUp_RefusesWhenTheInstanceAlreadyExists(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(state.Record{
		Name: "myinstance", Provider: "digitalocean",
		VMID: "vm-existing", IP: "192.0.2.9",
	}); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{vm: provider.VM{ID: "vm-new", IP: "192.0.2.1"}}
	err = Up(context.Background(), p, DefaultSteps(), "myinstance", cloudlabPath, repoRoot)
	if err == nil {
		t.Fatal("Up() = nil, want a refusal when the instance already exists")
	}
	if p.created {
		t.Error("Create was called; a second droplet was provisioned and the first orphaned")
	}
	if !strings.Contains(err.Error(), "192.0.2.9") {
		t.Errorf("error = %q, want it to name the existing instance's address", err.Error())
	}

	// The record must still describe the ORIGINAL droplet, or down can never
	// reach it again.
	got, ok, err := store.Get("myinstance")
	if err != nil || !ok {
		t.Fatalf("store.Get() = %v, %v; want the original record intact", ok, err)
	}
	if got.VMID != "vm-existing" {
		t.Errorf("record VMID = %q, want the original vm-existing -- the record was overwritten", got.VMID)
	}
}

func TestUp_RejectsInvalidInstanceNameBeforeCreate(t *testing.T) {
	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1"}}

	err := Up(context.Background(), p, DefaultSteps(), "my.instance", cloudlabPath, repoRoot)
	if err == nil {
		t.Fatal("Up() error = nil, want error for an invalid instance name")
	}
	if p.created {
		t.Error("p.Create was called despite an invalid instance name -- should fail before any expensive step")
	}
}

func TestUp_JoinsTailscaleWhenConfigEnablesIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := filepath.Join(repoRoot, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`tailscale = true`,
	}, "\n")+"\n")

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	var gotIP, gotUser string
	steps := Steps{
		WaitReady: func(ctx context.Context, ip, user string, timeout time.Duration) error { return nil },
		Reconcile: func(ctx context.Context, name, cloudlabPath string) error { return nil },
		JoinTailscale: func(ctx context.Context, ip, user string) error {
			gotIP, gotUser = ip, user
			return nil
		},
	}

	if err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if gotIP != "192.0.2.1" || gotUser == "" {
		t.Errorf("JoinTailscale called with (%q, %q)", gotIP, gotUser)
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("myinstance")
	if err != nil || !ok {
		t.Fatalf("state.Get(myinstance) ok=%v err=%v", ok, err)
	}
	if !record.TailscaleJoined {
		t.Error("record.TailscaleJoined = false, want true after a successful join")
	}
}

func TestUp_SkipsTailscaleWhenConfigDisablesIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	called := false
	steps := Steps{
		WaitReady:     func(ctx context.Context, ip, user string, timeout time.Duration) error { return nil },
		Reconcile:     func(ctx context.Context, name, cloudlabPath string) error { return nil },
		JoinTailscale: func(ctx context.Context, ip, user string) error { called = true; return nil },
	}

	if err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if called {
		t.Error("JoinTailscale was called despite tailscale defaulting to false")
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("myinstance")
	if err != nil || !ok {
		t.Fatalf("state.Get(myinstance) ok=%v err=%v", ok, err)
	}
	if record.TailscaleJoined {
		t.Error("record.TailscaleJoined = true, want false when tailscale was never enabled")
	}
}

func TestUp_StateRecordedBeforeWaitReady(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	steps := Steps{
		WaitReady: func(ctx context.Context, ip, user string, timeout time.Duration) error {
			return errors.New("simulated unreachable")
		},
		Reconcile: func(ctx context.Context, name, cloudlabPath string) error { return nil },
	}

	err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot)
	if err == nil {
		t.Fatal("Up() error = nil, want error when WaitReady fails")
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := store.Get("myinstance")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("state.Get(myinstance) = not found, want recorded even though WaitReady failed")
	}
}
