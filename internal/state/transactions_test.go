package state

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestStateUpdatePreservesConcurrentSessions(t *testing.T) {
	store := openTestStore(t)
	if err := store.Put(Record{Name: "instance"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, name := range []string{"auth", "docs"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := store.Update("instance", func(record *Record) error {
				record.PutSession(Session{Name: name})
				return nil
			}); err != nil {
				t.Errorf("Update(%s): %v", name, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	record, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth", "docs"} {
		if _, ok := record.FindSession(name); !ok {
			t.Fatalf("concurrent update lost %s: %+v", name, record.Sessions)
		}
	}
}

func TestStateAtomicWriteFailurePreservesPreviousJSON(t *testing.T) {
	store := openTestStore(t)
	if err := store.Put(Record{Name: "instance", IP: "old"}); err != nil {
		t.Fatal(err)
	}
	original := atomicRename
	atomicRename = func(_, _ string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { atomicRename = original })
	if err := store.Put(Record{Name: "instance", IP: "new"}); err == nil {
		t.Fatal("Put() succeeded through injected rename failure")
	}
	atomicRename = original
	record, ok, err := store.Get("instance")
	if err != nil || !ok || record.IP != "old" {
		t.Fatalf("Get() = %+v, %v, %v; want old JSON", record, ok, err)
	}
}

func TestSessionLockSerializesGenerationReplacement(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	release, err := store.LockSession(ctx, "instance", "auth")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	other, err := store.LockSession(ctx, "instance", "docs")
	if err != nil {
		t.Fatalf("independent session blocked: %v", err)
	}
	if err := other(); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if _, err := store.LockSession(waitCtx, "instance", "auth"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second auth lock = %v, want deadline", err)
	}
}

func TestInstanceLeaseExcludesDownButNotOtherInstances(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	shared, err := store.LockInstance(ctx, "one", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shared() }()
	other, err := store.LockInstance(ctx, "two", true)
	if err != nil {
		t.Fatalf("other instance blocked: %v", err)
	}
	_ = other()
	waitCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if _, err := store.LockInstance(waitCtx, "one", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive lease = %v, want deadline", err)
	}
}

func TestCompleteRetirementKeepsOtherSessions(t *testing.T) {
	store := openTestStore(t)
	generation := "00112233445566778899aabbccddeeff"
	record := Record{Name: "instance", Sessions: []Session{
		{Name: "auth", Generation: generation, AttachmentsKnown: true},
		{Name: "docs", Generation: "ffeeddccbbaa99887766554433221100", AttachmentsKnown: true},
	}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	receipt := RetirementReceipt{Session: "auth", Generation: generation, LocalRepo: "/repo", Operation: RetirementDelete, CompletedAt: time.Now()}
	if err := store.CompleteRetirement("instance", generation, receipt); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.FindSession("docs"); !ok {
		t.Fatal("completion removed the other session")
	}
	if _, ok := got.FindSession("auth"); ok {
		t.Fatal("completed generation remains active")
	}
	if _, ok := got.FindRetirementReceipt("auth", "/repo"); !ok {
		t.Fatal("completion receipt missing")
	}
	if err := store.CompleteRetirement("instance", generation, receipt); err == nil {
		t.Fatal("stale generation completion succeeded")
	}
}

func TestWriteFileAtomicPreservesMode(t *testing.T) {
	path := t.TempDir() + "/state"
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}
