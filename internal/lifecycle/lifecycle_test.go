package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// fakeProvider is a minimal provider.Provider test double: Create
// returns a canned VM (or err, if set); the other three methods are
// unused by Up but must exist to satisfy the interface.
type fakeProvider struct {
	vm      provider.VM
	err     error
	created bool
	gotSpec provider.InstanceSpec
}

func (f *fakeProvider) Create(ctx context.Context, spec provider.InstanceSpec) (provider.VM, error) {
	f.created = true
	f.gotSpec = spec
	return f.vm, f.err
}
func (f *fakeProvider) Destroy(ctx context.Context, id string) error { return nil }
func (f *fakeProvider) Get(ctx context.Context, id string) (provider.VM, error) {
	return provider.VM{}, nil
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

	var order []string
	steps := Steps{
		WaitReady: WaitReady,
		Reconcile: reconcile.Reconcile,
		Rsync: func(ctx context.Context, ip, localRepoRoot, remoteName string) error {
			order = append(order, "rsync")
			if ip != addr || localRepoRoot != repoRoot || remoteName != "myinstance" {
				t.Errorf("Rsync called with (%q, %q, %q)", ip, localRepoRoot, remoteName)
			}
			return nil
		},
		StartWatch: func(ctx context.Context, ip, name, localRepoRoot string) error {
			order = append(order, "watch")
			if ip != addr || name != "myinstance" || localRepoRoot != repoRoot {
				t.Errorf("StartWatch called with (%q, %q, %q)", ip, name, localRepoRoot)
			}
			return nil
		},
	}

	if err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if len(order) != 2 || order[0] != "rsync" || order[1] != "watch" {
		t.Errorf("call order = %v, want [rsync watch]", order)
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

func TestUp_StateRecordedBeforeWaitReady(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	steps := Steps{
		WaitReady: func(ctx context.Context, ip string, timeout time.Duration) error {
			return errors.New("simulated unreachable")
		},
		Reconcile:  func(ctx context.Context, name, cloudlabPath string) error { return nil },
		Rsync:      func(ctx context.Context, ip, localRepoRoot, remoteName string) error { return nil },
		StartWatch: func(ctx context.Context, ip, name, localRepoRoot string) error { return nil },
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
