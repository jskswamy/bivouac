package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// setupDownTest isolates state and Mutagen. Down's terminateWatch call
// starts a real Mutagen daemon even when terminating a session that
// doesn't exist (confirmed empirically against the real binary), so
// every Down test needs an isolated MUTAGEN_DATA_DIRECTORY and a
// cleanup that stops it -- otherwise tests leak a daemon rooted in the
// real developer machine's default data directory.
func setupDownTest(t *testing.T) *state.Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestDown_DestroysAndClearsState(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "127.0.0.1"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	if p.destroyedID != "vm-1" {
		t.Errorf("Destroy called with %q, want %q", p.destroyedID, "vm-1")
	}
	if _, ok, err := store.Get("myinstance"); err != nil || ok {
		t.Errorf("state record still present after Down (ok=%v, err=%v)", ok, err)
	}
}

func TestDown_MissingVMStillClearsState(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{destroyErr: fmt.Errorf("wrapped: %w", provider.ErrNotFound)}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v, want nil (not-found is success)", err)
	}
	if _, ok, _ := store.Get("myinstance"); ok {
		t.Error("state record still present after Down with ErrNotFound")
	}
}

func TestDown_RealDestroyErrorStillClearsStateButIsReturned(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{destroyErr: errors.New("network error")}
	err := Down(context.Background(), p, store, record)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok, _ := store.Get("myinstance"); ok {
		t.Error("state record still present after Down, want cleared even on real destroy error")
	}
}

func TestDown_DeregistersTailscaleWhenJoined(t *testing.T) {
	store := setupDownTest(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var gotCmd string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		gotCmd = cmd
		return "", 0
	})

	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser", TailscaleJoined: true}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	wantCmd := "bash -lc " + reconcile.ShellQuote("sudo tailscale logout")
	if gotCmd != wantCmd {
		t.Errorf("remote command = %q, want %q", gotCmd, wantCmd)
	}
}

// TestDown_LogsOutBeforeDestroying asserts the ordering guarantee
// documented on deregisterTailscale's call site in Down: logout must
// happen before Destroy, since once the VM is destroyed nothing can
// run on it anymore. A future accidental reorder of those two calls
// would otherwise slip past every other Down test, none of which
// observe relative order.
func TestDown_LogsOutBeforeDestroying(t *testing.T) {
	store := setupDownTest(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var order []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		order = append(order, "logout")
		return "", 0
	})

	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser", TailscaleJoined: true}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &orderedDestroyProvider{order: &order}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	want := []string{"logout", "destroy"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("call order = %v, want %v", order, want)
	}
}

// orderedDestroyProvider is a fakeProvider variant whose Destroy
// appends to a shared order slice, for TestDown_LogsOutBeforeDestroying
// only -- the package's regular fakeProvider (used by every other Down
// test) doesn't need this instrumentation.
type orderedDestroyProvider struct {
	fakeProvider
	order *[]string
}

func (p *orderedDestroyProvider) Destroy(ctx context.Context, id string) error {
	*p.order = append(*p.order, "destroy")
	return p.fakeProvider.Destroy(ctx, id)
}

func TestDown_SkipsTailscaleLogoutWhenNeverJoined(t *testing.T) {
	store := setupDownTest(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	called := false
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		called = true
		return "", 0
	})

	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if called {
		t.Error("tailscale logout was run despite TailscaleJoined being false")
	}
}
