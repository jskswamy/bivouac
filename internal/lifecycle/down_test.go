package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

// setupDownTest isolates state so each Down test gets its own store
// rather than sharing the real developer machine's default location.
func setupDownTest(t *testing.T) *state.Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
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
	if err := Down(context.Background(), p, store, record, false); err != nil {
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
	if err := Down(context.Background(), p, store, record, false); err != nil {
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
	err := Down(context.Background(), p, store, record, false)
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
		if strings.Contains(cmd, "command -v tailscale") {
			return "/home/devuser/.nix-profile/bin/tailscale\n", 0
		}
		gotCmd = cmd
		return "", 0
	})

	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser", TailscaleJoined: true}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record, false); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	// Absolute path, not a bare name: sudo resets PATH to its own
	// secure_path, which has no ~/.nix-profile/bin in it.
	if !strings.Contains(gotCmd, "/home/devuser/.nix-profile/bin/tailscale") {
		t.Errorf("remote command = %q, want tailscale invoked by absolute path", gotCmd)
	}
	if !strings.Contains(gotCmd, "logout") {
		t.Errorf("remote command = %q, want a tailscale logout", gotCmd)
	}
	if strings.Contains(gotCmd, "sudo tailscale") {
		t.Errorf("remote command = %q, must not invoke a bare `sudo tailscale`", gotCmd)
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
	if err := Down(context.Background(), p, store, record, false); err != nil {
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
	if err := Down(context.Background(), p, store, record, false); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if called {
		t.Error("tailscale logout was run despite TailscaleJoined being false")
	}
}

// Destroying an instance is irreversible, so a rescue that fails means we
// do not know the work is safe -- and not knowing must be treated as not
// safe.
func TestDown_AbortsWhenRescueFails(t *testing.T) {
	store := setupDownTest(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "checkpoint exploded", 1
	})

	// A session must be recorded: an empty Sessions slice tells
	// rescueBeforeDestroy no session was ever started, and it would skip
	// the rescue (and this test's fake SSH server) entirely.
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser"}
	record.PutSession(state.Session{Name: "auth", LocalRepo: t.TempDir()})
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	err := Down(context.Background(), p, store, record, false)
	if err == nil {
		t.Fatal("Down() = nil, want an error when work could not be rescued")
	}
	if p.destroyed {
		t.Error("Down() destroyed the VM despite a failed rescue")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to name the --force escape hatch", err.Error())
	}
}

// down must rescue every session. Rescuing one and destroying the droplet
// holding three is the data loss this whole design exists to prevent.
func TestDown_RescuesEverySessionBeforeDestroying(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var rescued []string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		// The checkpoint names its session in the commit subject.
		if strings.Contains(cmd, "checkpoint") {
			for _, n := range []string{"auth", "docs"} {
				if strings.Contains(cmd, n) {
					rescued = append(rescued, n)
				}
			}
		}
		return "", 0
	})

	record := state.Record{Name: "inst", IP: addr, User: "devuser"}
	record.PutSession(state.Session{Name: "auth", LocalRepo: t.TempDir()})
	record.PutSession(state.Session{Name: "docs", LocalRepo: t.TempDir()})

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	// Rescue fails (no real git remote), so this must refuse to destroy --
	// which is also what proves both sessions were attempted.
	_ = Down(context.Background(), p, store, record, false)

	if len(rescued) < 2 {
		t.Errorf("checkpointed %v, want both auth and docs attempted", rescued)
	}
	if p.destroyed {
		t.Error("the instance was destroyed even though a rescue failed")
	}
}

// A broken or unreachable box must still be destroyable, or the user is
// left paying for a VM they cannot delete.
func TestDown_ForceDestroysWithoutRescuing(t *testing.T) {
	store := setupDownTest(t)
	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: "203.0.113.5", User: "devuser"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record, true); err != nil {
		t.Fatalf("Down(force) error = %v, want success", err)
	}
	if !p.destroyed {
		t.Error("Down(force) did not destroy the VM")
	}
}

// The end-to-end version of the beads guard for down: exercises the real
// rescueBeforeDestroy call site, including the added `continue` and
// RemoteRepoPath argument order (record.Name plays repoName here, unlike
// delete's own s.Name/repoName pairing), rather than the guard function in
// isolation.
func TestDown_RefusesWhenIssuesHaveNotLanded(t *testing.T) {
	requireBd(t)
	f := newSessionFixture(t, 0)
	bdInitStealth(t, f.repo)
	if err := beads.Seed(context.Background(), f.repo, f.session, beads.FileURL(f.agent)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	// Every other instance command (checkpoint, rev-parse, rm -rf) keeps
	// working -- the rescue itself must still succeed -- only the dolt push
	// fails.
	f.cmdOverride = func(cmd string) (string, uint32, bool) {
		if strings.Contains(cmd, "dolt") && strings.Contains(cmd, "push") {
			return "dolt push exploded", 1, true
		}
		return "", 0, false
	}

	store := setupDownTest(t)
	record := state.Record{Name: f.repoName, IP: f.addr, User: "devuser"}
	record.PutSession(state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base})
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	err := Down(context.Background(), p, store, record, false)
	if err == nil {
		t.Fatal("Down() = nil when the session's issues had not landed, want a refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to name the --force escape hatch", err.Error())
	}
	if p.destroyed {
		t.Error("Down() destroyed the VM despite unlanded issues")
	}
}

// The other half: once the sync completes, down must still proceed. Without
// this, TestDown_RefusesWhenIssuesHaveNotLanded could be "explained" by a
// guard that refuses unconditionally once beads is wired at all.
func TestDown_ProceedsWhenIssuesHaveLanded(t *testing.T) {
	requireBd(t)
	f := newSessionFixture(t, 0)
	bdInitStealth(t, f.repo)
	if err := beads.Seed(context.Background(), f.repo, f.session, beads.FileURL(f.agent)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	// No override: every instance command, dolt push included, succeeds.

	store := setupDownTest(t)
	record := state.Record{Name: f.repoName, VMID: "vm-1", IP: f.addr, User: "devuser"}
	record.PutSession(state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base})
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record, false); err != nil {
		t.Fatalf("Down() error = %v, want success once beads' issues have landed", err)
	}
	if p.destroyedID != "vm-1" {
		t.Errorf("Destroy called with %q, want %q", p.destroyedID, "vm-1")
	}
}

// Proves the `continue` rescueBeforeDestroy's loop needs: once a session's
// own rescue has already failed, checking its issues too would only spend a
// second, doomed round trip on a box already known to be a problem. This
// asserts on the round trip itself (whether the dolt push the beads sync
// depends on was ever attempted), because firstErr alone can't tell the two
// behaviours apart -- a later error is guarded from overwriting the first
// regardless of whether the check ran.
func TestDown_SkipsBeadsCheckForASessionWhoseRescueFailed(t *testing.T) {
	requireBd(t)
	f := newSessionFixture(t, 0)
	bdInitStealth(t, f.repo)
	if err := beads.Seed(context.Background(), f.repo, f.session, beads.FileURL(f.agent)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	var pushAttempted bool
	f.cmdOverride = func(cmd string) (string, uint32, bool) {
		if strings.Contains(cmd, "git add -A") {
			// Fails the checkpoint, and so RescueSession itself.
			return "checkpoint exploded", 1, true
		}
		if strings.Contains(cmd, "dolt") && strings.Contains(cmd, "push") {
			f.mu.Lock()
			pushAttempted = true
			f.mu.Unlock()
			return "", 0, true
		}
		return "", 0, false
	}

	store := setupDownTest(t)
	record := state.Record{Name: f.repoName, IP: f.addr, User: "devuser"}
	record.PutSession(state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base})
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record, false); err == nil {
		t.Fatal("Down() = nil despite a failed rescue, want the rescue error")
	}

	f.mu.Lock()
	attempted := pushAttempted
	f.mu.Unlock()
	if attempted {
		t.Error("the beads sync ran for a session whose rescue had already failed -- the `continue` did not skip it")
	}
}

// wireBeadsOnlyRepo gives dir its own real, minimal beads database and
// dolt remote for session, without any of the git session-remote plumbing
// sessionFixture sets up: RescueSession's checkpoint and rev-parse succeed
// against the fake SSH server regardless, so the local `git fetch
// cloudlab-<session>` that follows -- with no such remote ever
// registered -- is what makes the rescue fail. That is deliberately the
// cheapest way to fail a rescue while still making beads.Wired true.
func wireBeadsOnlyRepo(t *testing.T, dir, session string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir)
	bdInitStealth(t, dir)
	sink := t.TempDir()
	initRepo(t, sink)
	if err := beads.Seed(context.Background(), dir, session, beads.FileURL(sink)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
}

// The multi-session counterpart of TestDown_SkipsBeadsCheckForASessionWhoseRescueFailed:
// once a second session's rescue also fails, firstErr is already set, so the
// `err != nil && firstErr == nil` gate that used to guard the `continue` went
// false right along with it, and the beads check ran anyway on a box already
// known to be broken. Fixed by splitting "skip the beads check" from "record
// only the first error" into two separate conditions in down.go.
func TestDown_SkipsBeadsCheckForEverySessionWhoseRescueFailed(t *testing.T) {
	requireBd(t)
	startFakeAgent(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	sessions := []string{"auth", "docs"}
	repos := make(map[string]string, len(sessions))
	for _, s := range sessions {
		dir := filepath.Join(base, s)
		wireBeadsOnlyRepo(t, dir, s)
		repos[s] = dir
	}

	var mu sync.Mutex
	pushAttempted := map[string]bool{}
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		if strings.Contains(cmd, "dolt") && strings.Contains(cmd, "push") {
			mu.Lock()
			for _, s := range sessions {
				if strings.Contains(cmd, s) {
					pushAttempted[s] = true
				}
			}
			mu.Unlock()
		}
		return "", 0
	})

	record := state.Record{Name: "repo", IP: addr, User: "devuser"}
	for _, s := range sessions {
		record.PutSession(state.Session{Name: s, LocalRepo: repos[s]})
	}

	store := setupDownTest(t)
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record, false); err == nil {
		t.Fatal("Down() = nil despite both sessions failing rescue, want the rescue error")
	}
	if p.destroyed {
		t.Error("Down() destroyed the VM despite both sessions failing rescue")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, s := range sessions {
		if pushAttempted[s] {
			t.Errorf("the beads sync ran for session %s, whose rescue had already failed", s)
		}
	}
}
