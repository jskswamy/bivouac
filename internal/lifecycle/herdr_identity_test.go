package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
)

func TestHerdrSessionsUseMachineAlias(t *testing.T) {
	ctx, c := transportFixture(t)
	for _, session := range []string{"one", "two", "three"} {
		target, err := MachineTarget(ctx, c.PublicIP, c.User)
		if err != nil {
			t.Fatal(err)
		}
		if target != "ssh://me@bivouac-machine" {
			t.Fatalf("session %s target=%q", session, target)
		}
	}
	second := c
	second.Name = "other"
	second.Alias = "bivouac-other"
	second.PublicIP = "192.0.2.2"
	second.PreferredHost = second.PublicIP
	target, err := MachineTarget(sshidentity.WithConnection(context.Background(), second), second.PublicIP, second.User)
	if err != nil || target != "ssh://me@bivouac-other" {
		t.Fatalf("second target=%q: %v", target, err)
	}
	legacy, err := MachineTarget(context.Background(), c.PublicIP, c.User)
	if err != nil || legacy != "ssh://me@192.0.2.1" {
		t.Fatalf("legacy=%q: %v", legacy, err)
	}
}

func TestEnsureMachineFindsRenamedOwnedAliasAfterInterruptedRecording(t *testing.T) {
	target := "ssh://me@bivouac-machine"
	h := &fakeHerdr{listJSON: `[{"id":"id","label":"my renamed machine","target":"ssh://me@bivouac-machine","session":"default","enabled":true}]`}
	id, label, err := EnsureMachine(h, "machine", target, "")
	if err != nil || id != "id" || label != "my renamed machine" || h.ran("add") {
		t.Fatalf("id=%q label=%q err=%v calls=%v", id, label, err, h.calls)
	}
}

func TestSelectedMachineRejectsRecordedTargetMismatch(t *testing.T) {
	h := &fakeHerdr{listJSON: `[{"id":"id","label":"machine","target":"ssh://me@bivouac-other","session":"default","enabled":true}]`}
	if _, _, err := EnsureMachine(h, "machine", "ssh://me@bivouac-machine", "id"); err == nil {
		t.Fatal("recorded id silently used another machine's identity")
	}
}

func TestSelectedDownKeepsProfileOnDestroyFailure(t *testing.T) {
	ctx, c := transportFixture(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Record{Name: c.Name, IP: c.PublicIP, User: c.User, VMID: "vm", SSH: &c}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{destroyErr: errors.New("provider unavailable")}
	if err := Down(ctx, p, store, record, true); err == nil {
		t.Fatal("destroy failure hidden")
	}
	if _, ok, err := store.Get(c.Name); err != nil || !ok {
		t.Fatalf("tracked identity lost: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(profiles.ConfigDir, "ssh", "config"))
	if err != nil || !strings.Contains(string(data), c.Alias) {
		t.Fatalf("alias lost: %v", err)
	}
}

func TestSelectedDownRemovesOnlyDestroyedMachineProfile(t *testing.T) {
	ctx, c := transportFixture(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Record{Name: c.Name, IP: c.PublicIP, User: c.User, VMID: "vm", SSH: &c}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	second := c
	second.Name = "other"
	second.Alias = "bivouac-other"
	if err := profiles.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := Down(ctx, &fakeProvider{}, store, record, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(profiles.ConfigDir, "ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Host "+c.Alias+"\n") || !strings.Contains(string(data), second.Alias) {
		t.Fatalf("incorrect cleanup: %s", data)
	}
}

func TestSessionCleanupLeavesMachineSSHProfile(t *testing.T) {
	ctx, c := transportFixture(t)
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	previous := removeHerdrMachine
	removeHerdrMachine = func(herdrRunner, string) error { return nil }
	t.Cleanup(func() { removeHerdrMachine = previous })
	CleanupHerdr(ctx, "owned-id", "one", nil)
	data, err := os.ReadFile(filepath.Join(profiles.ConfigDir, "ssh", "config"))
	if err != nil || !strings.Contains(string(data), c.Alias) {
		t.Fatalf("session removed machine alias: %v", err)
	}
}
