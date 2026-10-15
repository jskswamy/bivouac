package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/state"
)

func TestSessionGenerationSurvivesStartRetry(t *testing.T) {
	repo := newTestRepo(t)
	record := state.Record{Name: "instance", IP: "127.0.0.1:1", User: "devuser"}
	store := sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := cmd.Flags().Set("repo", repo); err != nil {
		t.Fatal(err)
	}
	if err := runSessionStart(cmd, "instance", []string{"auth"}); err == nil {
		t.Fatal("first start unexpectedly succeeded")
	}
	first, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := first.FindSession("auth")
	if !ok || sess.Generation == "" || !sess.AttachmentsKnown {
		t.Fatalf("first start session = %+v, %v", sess, ok)
	}
	generation := sess.Generation
	sess.Attachments = []state.HerdrAttachment{{
		ID: "owned", Kind: state.AttachmentWorkspace,
		Address: state.HerdrAddress{MachineID: "mid", Session: "default", ConfigRoot: "~/.config/herdr"},
		Handle:  "w1", Owned: true, Phase: state.AttachmentActive,
		Proof: state.OwnershipProof{Scheme: "herdr-workspace-root-terminal-v1", Data: json.RawMessage(`{"terminalId":"term"}`)},
	}}
	first.PutSession(sess)
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}

	if err := runSessionStart(cmd, "instance", []string{"auth"}); err == nil {
		t.Fatal("retry unexpectedly succeeded")
	}
	got, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	retried, ok := got.FindSession("auth")
	if !ok || retried.Generation != generation {
		t.Fatalf("retry generation = %q, want %q", retried.Generation, generation)
	}
	if len(retried.Attachments) != 1 || retried.Attachments[0].ID != "owned" {
		t.Fatalf("retry erased lifecycle state: %+v", retried)
	}
}

func TestRecreatedSessionDoesNotUseOldReceipt(t *testing.T) {
	// Receipts written by the CLI use the resolved repository root, including
	// macOS's /var -> /private/var symlink.
	repo, err := filepath.EvalSymlinks(newTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration := "00112233445566778899aabbccddeeff"
	record := state.Record{
		Name: "instance", IP: "127.0.0.1:1", User: "devuser",
		RetirementReceipts: []state.RetirementReceipt{{
			Session: "auth", Generation: oldGeneration, LocalRepo: repo,
			Operation: state.RetirementMerge, CompletedAt: time.Now(),
		}},
	}
	store := sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := cmd.Flags().Set("repo", repo); err != nil {
		t.Fatal(err)
	}
	if err := runSessionStart(cmd, "instance", []string{"auth"}); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	got, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	active, ok := got.FindSession("auth")
	if !ok {
		t.Fatal("new active generation missing")
	}
	if active.Generation == "" || active.Generation == oldGeneration {
		t.Fatalf("active generation = %q, old = %q", active.Generation, oldGeneration)
	}
	if active.LocalRepo != repo {
		t.Fatalf("active repository = %q, want %q", active.LocalRepo, repo)
	}
	if _, ok := got.FindRetirementReceipt("auth", repo); ok {
		t.Fatal("old completion receipt survived active generation replacement")
	}
}

func TestCompletedRetirementCommandIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	record := state.Record{
		Name: "instance", IP: "127.0.0.1:1", User: "devuser",
		RetirementReceipts: []state.RetirementReceipt{{
			Session: "auth", Generation: "00112233445566778899aabbccddeeff", LocalRepo: repo,
			Operation: state.RetirementMerge, CompletedAt: time.Now(),
		}},
	}
	sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := runMerge(cmd, "wrong-cwd-derived-name", []string{"auth"}); err != nil {
		t.Fatalf("completed merge retry = %v", err)
	}
	if !strings.Contains(cmd.OutOrStdout().(*bytes.Buffer).String(), "already merged") {
		t.Fatal("completed retry did not report prior completion")
	}
}

func TestSessionStartRejectsPendingRetirement(t *testing.T) {
	repo := newTestRepo(t)
	record := state.Record{Name: "instance", IP: "127.0.0.1:1", User: "devuser"}
	record.PutSession(state.Session{
		Name: "auth", LocalRepo: repo, Base: "base", RepoName: "repo",
		Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
		Retirement: &state.Retirement{
			Version: state.SessionLifecycleVersion, Operation: state.RetirementDelete,
			Phase:       state.RetirementPaused,
			Destination: state.Destination{LocalRepo: repo, GitDir: repo + "/.git", Branch: "main"},
			Effects:     map[string]state.RetirementEffect{},
		},
	})
	store := sessionTestStore(t, record)
	cmd := sessionTestCmd()
	if err := cmd.Flags().Set("repo", repo); err != nil {
		t.Fatal(err)
	}
	err := runSessionStart(cmd, "instance", []string{"auth"})
	if err == nil || !strings.Contains(err.Error(), "retirement") {
		t.Fatalf("runSessionStart() = %v, want pending retirement refusal", err)
	}
	got, _, getErr := store.Get("instance")
	if getErr != nil {
		t.Fatal(getErr)
	}
	sess, _ := got.FindSession("auth")
	if sess.Retirement == nil || sess.Generation != "00112233445566778899aabbccddeeff" {
		t.Fatalf("pending lifecycle was overwritten: %+v", sess)
	}
}
