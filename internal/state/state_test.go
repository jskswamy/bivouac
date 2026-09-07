package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s
}

func TestList_EmptyWhenFileMissing(t *testing.T) {
	s := openTestStore(t)
	records, err := s.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Errorf("List() = %v, want empty", records)
	}
}

func TestPutGetList_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	rec := Record{Name: "myrepo", Provider: "digitalocean", VMID: "123", IP: "1.2.3.4"}

	if err := s.Put(rec); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, ok, err := s.Get("myrepo")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("Get() = %+v, want %+v", got, rec)
	}

	all, err := s.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 || !reflect.DeepEqual(all[0], rec) {
		t.Errorf("List() = %+v, want [%+v]", all, rec)
	}
}

func TestDelete_RemovesRecord(t *testing.T) {
	s := openTestStore(t)
	if err := s.Put(Record{Name: "myrepo"}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := s.Delete("myrepo"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, ok, err := s.Get("myrepo")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ok {
		t.Error("Get() ok = true after Delete, want false")
	}
}

func TestPut_NullStateFileDoesNotPanic(t *testing.T) {
	s := openTestStore(t)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := Record{Name: "myrepo", Provider: "digitalocean"}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, ok, err := s.Get("myrepo")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("Get() = %+v, want %+v", got, rec)
	}
}

func TestList_CorruptFileErrors(t *testing.T) {
	s := openTestStore(t)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.List(); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRecord_PutSessionReplacesByNameAndAppendsOtherwise(t *testing.T) {
	var r Record
	r.PutSession(Session{Name: "auth", LocalRepo: "/repo", Base: "aaa"})
	r.PutSession(Session{Name: "docs", LocalRepo: "/repo", Base: "bbb"})
	if len(r.Sessions) != 2 {
		t.Fatalf("Sessions = %d, want 2", len(r.Sessions))
	}

	// Same name replaces rather than duplicating -- a retried start must not
	// leave two entries whose bases disagree.
	r.PutSession(Session{Name: "auth", LocalRepo: "/repo", Base: "ccc"})
	if len(r.Sessions) != 2 {
		t.Fatalf("Sessions = %d after replacing auth, want 2", len(r.Sessions))
	}
	got, ok := r.FindSession("auth")
	if !ok || got.Base != "ccc" {
		t.Errorf("FindSession(auth) = %+v, %v; want Base ccc", got, ok)
	}

	// Order is stable: list output should not shuffle between runs.
	if r.Sessions[0].Name != "auth" || r.Sessions[1].Name != "docs" {
		t.Errorf("Sessions order = %v, want auth then docs", r.Sessions)
	}
}

// The record round-trips through the store, so the new shape must persist.
func TestStore_RoundTripsSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	r := Record{Name: "inst"}
	r.PutSession(Session{Name: "auth", LocalRepo: "/repo", Base: "aaa"})
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("inst")
	if err != nil || !ok {
		t.Fatalf("Get() = %v, %v", ok, err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Base != "aaa" {
		t.Errorf("round-tripped sessions = %+v, want one with Base aaa", got.Sessions)
	}
}
