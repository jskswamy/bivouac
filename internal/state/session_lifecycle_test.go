package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionLifecycleRoundTrip(t *testing.T) {
	store := openTestStore(t)
	proof := json.RawMessage(`{"terminalId":"term_owned"}`)
	record := Record{
		Name: "instance",
		Sessions: []Session{{
			Name:             "auth",
			LocalRepo:        "/repo",
			Base:             "base",
			RepoName:         "owner/repo",
			Generation:       "00112233445566778899aabbccddeeff",
			AttachmentsKnown: true,
			Attachments: []HerdrAttachment{
				{ID: "one", Kind: AttachmentWorkspace, Address: HerdrAddress{MachineID: "m1", Session: "default", ConfigRoot: "~/.config/herdr"}, Handle: "w1", Proof: OwnershipProof{Scheme: "herdr-workspace-root-terminal-v1", Data: proof}, Owned: true, Phase: AttachmentActive},
				{ID: "two", Kind: AttachmentNamedSession, Address: HerdrAddress{Session: "auth", ConfigRoot: "~/.config/herdr"}, Handle: "auth", Proof: OwnershipProof{Scheme: "herdr-named-session-v1", Data: json.RawMessage(`{"name":"auth"}`)}, Owned: true, Phase: AttachmentClosing},
			},
			Retirement: &Retirement{
				Version:   SessionLifecycleVersion,
				Operation: RetirementMerge,
				Phase:     RetirementPaused,
				Destination: Destination{
					LocalRepo: "/repo",
					GitDir:    "/repo/.git",
					Branch:    "main",
				},
				Accepted:    []Acceptance{{SourceTip: "source", ReplayBase: "base", VerifiedHead: "head", IssueEvidence: "issues"}},
				Effects:     map[string]RetirementEffect{"herdr:one": {Status: EffectDone}, "remote-checkout": {Status: EffectIntended}},
				LastFailure: "close interrupted",
			},
		}},
		RetirementReceipts: []RetirementReceipt{{
			Session: "docs", Generation: "ffeeddccbbaa99887766554433221100", LocalRepo: "/repo", Operation: RetirementDelete, Signed: []string{"abc"}, CompletedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC),
		}},
	}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(record.Name)
	if err != nil || !ok {
		t.Fatalf("Get() = %v, %v", ok, err)
	}
	for i := range record.Sessions[0].Attachments {
		wantProof := record.Sessions[0].Attachments[i].Proof.Data
		gotProof := got.Sessions[0].Attachments[i].Proof.Data
		if !json.Valid(gotProof) || !reflect.DeepEqual(mustJSONValue(t, gotProof), mustJSONValue(t, wantProof)) {
			t.Fatalf("attachment %d proof changed: got %s, want %s", i, gotProof, wantProof)
		}
		record.Sessions[0].Attachments[i].Proof.Data = gotProof
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, record)
	}
}

func TestLegacyAttachmentStateRemainsUnknown(t *testing.T) {
	store := openTestStore(t)
	legacy := `{"instance":{"name":"instance","sessions":[{"name":"auth","local_repo":"/repo","base":"base","repo_name":"owner/repo","herdr_machine_id":"legacy-mid"}]}}`
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("instance")
	if err != nil || !ok {
		t.Fatalf("Get() = %v, %v", ok, err)
	}
	sess, ok := record.FindSession("auth")
	if !ok {
		t.Fatal("legacy session missing")
	}
	if sess.AttachmentsKnown {
		t.Fatal("legacy ownership must remain unknown")
	}
	if sess.HerdrMachineID != "legacy-mid" {
		t.Fatalf("legacy HerdrMachineID = %q", sess.HerdrMachineID)
	}
	if sess.RepoNameOr("instance-name") != "owner/repo" {
		t.Fatal("lost checkout identity")
	}
}

func TestNewSessionGenerationIsRandomAndValid(t *testing.T) {
	one, err := NewSessionGeneration()
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewSessionGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if one == two || len(one) != 32 || len(two) != 32 {
		t.Fatalf("generations = %q, %q", one, two)
	}
	if err := ValidateSessionLifecycle(Session{Name: "auth", Generation: one, AttachmentsKnown: true}); err != nil {
		t.Fatalf("generated identity rejected: %v", err)
	}
}

func TestValidateSessionLifecycleRejectsCorruptRequiredState(t *testing.T) {
	base := Session{Name: "auth", Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true}
	tests := []struct {
		name string
		edit func(*Session)
		want string
	}{
		{"unsupported version", func(s *Session) { s.Retirement = validRetirement(); s.Retirement.Version++ }, "version"},
		{"force merge", func(s *Session) {
			s.Retirement = validRetirement()
			s.Retirement.Operation = RetirementMerge
			s.Retirement.Force = true
		}, "force"},
		{"unknown phase", func(s *Session) { s.Retirement = validRetirement(); s.Retirement.Phase = RetirementPhase("mystery") }, "phase"},
		{"attachment without known state", func(s *Session) { s.AttachmentsKnown = false; s.Attachments = []HerdrAttachment{{ID: "a"}} }, "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := base
			test.edit(&sess)
			if err := ValidateSessionLifecycle(sess); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSessionLifecycle() = %v, want %q", err, test.want)
			}
		})
	}
}

func mustJSONValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func validRetirement() *Retirement {
	return &Retirement{
		Version: SessionLifecycleVersion, Operation: RetirementDelete, Phase: RetirementChecking,
		Destination: Destination{LocalRepo: "/repo", GitDir: "/repo/.git", Branch: "main"},
		Effects:     map[string]RetirementEffect{},
	}
}
