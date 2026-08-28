package state

import (
	"os"
	"path/filepath"
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
	if got != rec {
		t.Errorf("Get() = %+v, want %+v", got, rec)
	}

	all, err := s.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 || all[0] != rec {
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
	if got != rec {
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
