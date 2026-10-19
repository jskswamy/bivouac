package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
)

type closeableOwnershipRunner struct {
	replies map[string][]string
}

func (r *closeableOwnershipRunner) Run(command string) (string, error) {
	queue := r.replies[command]
	if len(queue) == 0 {
		return "", nil
	}
	if len(queue) > 1 {
		r.replies[command] = queue[1:]
	}
	return queue[0], nil
}

func (r *closeableOwnershipRunner) Close() error { return nil }

type ownershipRunner struct {
	replies    map[string][]string
	calls      []string
	fail       map[string]error
	autoCreate bool
}

func (r *ownershipRunner) Run(args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, key)
	if err := r.fail[key]; err != nil {
		return "failed", err
	}
	if r.autoCreate && len(args) > 4 && args[2] == "workspace" && args[3] == "create" {
		label := ""
		for i := range args {
			if args[i] == "--label" && i+1 < len(args) {
				label = args[i+1]
			}
		}
		return fmt.Sprintf(`{"result":{"workspace":{"workspace_id":"w1","label":%q},"root_pane":{"pane_id":"w1:p1","terminal_id":"term-owned"}}}`, label), nil
	}
	queue := r.replies[key]
	if len(queue) == 0 {
		return "", nil
	}
	if len(queue) > 1 {
		r.replies[key] = queue[1:]
	}
	return queue[0], nil
}

func ownedWorkspaceAttachment(t *testing.T) state.HerdrAttachment {
	t.Helper()
	proof, err := json.Marshal(workspaceTerminalEvidence{PaneID: "w1:p1", TerminalID: "term-owned"})
	if err != nil {
		t.Fatal(err)
	}
	return state.HerdrAttachment{
		ID: "attachment", Kind: state.AttachmentWorkspace,
		Address: state.HerdrAddress{MachineID: "mid", Session: "default", ConfigRoot: herdrConfigRoot},
		Handle:  "w1", Proof: state.OwnershipProof{Scheme: workspaceTerminalProof, Data: proof},
		Owned: true, Phase: state.AttachmentActive,
	}
}

func machineReply() string {
	return `[{"id":"mid","label":"instance","target":"ssh://u@host","session":"default","enabled":true}]`
}

func workspaceReply(id, label string) string {
	return fmt.Sprintf(`{"result":{"workspaces":[{"workspace_id":%q,"label":%q,"pane_count":1}]}}`, id, label)
}

func panesReply(terminal string) string {
	return fmt.Sprintf(`{"result":{"panes":[{"pane_id":"w1:p1","terminal_id":%q,"workspace_id":"w1"}]}}`, terminal)
}

func withAttachmentRunner(t *testing.T, runner herdrRunner) {
	t.Helper()
	old := attachmentHerdrRunner
	attachmentHerdrRunner = func(context.Context) herdrRunner { return runner }
	t.Cleanup(func() { attachmentHerdrRunner = old })
}

func TestDirectAttachRecordsNamedServer(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Record{Name: "instance", IP: "host", User: "u", Sessions: []state.Session{{
		Name: "auth", LocalRepo: "/repo", Base: "base", RepoName: "repo",
		Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
	}}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	runner := &ownershipRunner{autoCreate: true, replies: map[string][]string{
		"machine list --json":                    {`[{"id":"mid","label":"instance","target":"ssh://u@host","session":"default","enabled":true}]`},
		"--machine mid workspace list":           {`{"result":{"workspaces":[]}}`},
		"--machine mid workspace rename w1 auth": {`{"result":{"type":"ok"}}`},
		"--machine mid workspace focus w1":       {`{"result":{"type":"ok"}}`},
	}}
	oldRunner, oldRequire := attachmentHerdrRunner, requireHerdrTool
	attachmentHerdrRunner = func(context.Context) herdrRunner { return runner }
	requireHerdrTool = func(string) (string, error) { return "/fixture/herdr", nil }
	t.Cleanup(func() { attachmentHerdrRunner, requireHerdrTool = oldRunner, oldRequire })

	attachment, err := EnsureSessionAttachment(context.Background(), store, "instance", "auth")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Address.Session != "default" || attachment.Address.MachineID != "mid" || attachment.Handle != "w1" {
		t.Fatalf("attachment = %+v", attachment)
	}
	got, _, err := store.Get("instance")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := got.FindSession("auth")
	if len(sess.Attachments) != 1 || sess.Attachments[0].Phase != state.AttachmentActive {
		t.Fatalf("recorded attachments = %+v", sess.Attachments)
	}
}

func TestUnknownLegacyAttachmentNeedsConfirmation(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Record{Name: "instance", Sessions: []state.Session{{Name: "auth"}}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureSessionAttachment(context.Background(), store, "instance", "auth")
	if err == nil || !strings.Contains(err.Error(), "confirm adoption interactively") {
		t.Fatalf("EnsureSessionAttachment() = %v", err)
	}
}

func TestAttachmentIntentRecoversLostCreateReply(t *testing.T) {
	runner := &ownershipRunner{replies: map[string][]string{
		"--machine mid workspace list":           {workspaceReply("w1", attachmentIntentLabel("token"))},
		"--machine mid pane list --workspace w1": {panesReply("term-owned")},
	}}
	workspace, proof, found, err := recoverIntendedWorkspace(runner,
		state.HerdrAddress{MachineID: "mid", Session: "default", ConfigRoot: herdrConfigRoot}, attachmentIntentLabel("token"))
	if err != nil || !found {
		t.Fatalf("recover = %+v, %+v, %v, %v", workspace, proof, found, err)
	}
	if workspace.ID != "w1" || proof.PaneID != "w1:p1" || proof.TerminalID != "term-owned" {
		t.Fatalf("recovered wrong ownership: %+v %+v", workspace, proof)
	}
}

func TestAttachmentRenameKeepsOwnership(t *testing.T) {
	attachment := ownedWorkspaceAttachment(t)
	runner := &ownershipRunner{replies: map[string][]string{
		"--machine mid pane list --workspace w1": {panesReply("term-owned")},
	}}
	if err := verifyWorkspaceOwnership(runner, attachment); err != nil {
		t.Fatalf("renamed workspace proof rejected: %v", err)
	}
}

func TestCloseAttachmentRefusesSameLabelForeignResource(t *testing.T) {
	attachment := ownedWorkspaceAttachment(t)
	runner := &ownershipRunner{replies: map[string][]string{
		"machine list --json":                    {machineReply()},
		"--machine mid workspace list":           {workspaceReply("w1", "auth")},
		"--machine mid pane list --workspace w1": {panesReply("term-foreign")},
	}}
	withAttachmentRunner(t, runner)
	record := state.Record{Name: "instance", HerdrMachineID: "mid"}
	if err := CloseSessionAttachment(context.Background(), record, attachment); err == nil || !strings.Contains(err.Error(), "ownership proof") {
		t.Fatalf("CloseSessionAttachment() = %v, want ownership refusal", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "workspace close") {
			t.Fatalf("closed foreign same-label resource: %v", runner.calls)
		}
	}
}

func TestCloseAttachmentAfterIDReuse(t *testing.T) {
	attachment := ownedWorkspaceAttachment(t)
	runner := &ownershipRunner{replies: map[string][]string{
		"machine list --json":                    {machineReply()},
		"--machine mid workspace list":           {workspaceReply("w1", "replacement")},
		"--machine mid pane list --workspace w1": {panesReply("term-replacement")},
	}}
	withAttachmentRunner(t, runner)
	record := state.Record{Name: "instance", HerdrMachineID: "mid"}
	if err := CloseSessionAttachment(context.Background(), record, attachment); err == nil {
		t.Fatal("stale proof closed replacement")
	}
}

func TestDirectAttachRecordsNamedSessionIntent(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Record{Name: "instance", Sessions: []state.Session{{
		Name: "auth", Generation: "00112233445566778899aabbccddeeff", AttachmentsKnown: true,
	}}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	attachment, err := BeginDirectSessionAttachment(context.Background(), store, "instance", "auth")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Kind != state.AttachmentNamedSession || attachment.Address.Session != "auth" || attachment.Phase != state.AttachmentCreating {
		t.Fatalf("direct attachment = %+v", attachment)
	}
	if err := CompleteDirectSessionAttachment(store, "instance", record.Sessions[0].Generation, attachment); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get("instance")
	sess, _ := got.FindSession("auth")
	if len(sess.Attachments) != 1 || sess.Attachments[0].Phase != state.AttachmentActive {
		t.Fatalf("recorded direct attachment = %+v", sess.Attachments)
	}
}

func TestCloseNamedSessionObservesAbsence(t *testing.T) {
	proof, _ := json.Marshal(namedSessionEvidence{Session: "auth", Token: "token"})
	attachment := state.HerdrAttachment{
		ID: "token", Kind: state.AttachmentNamedSession,
		Address: state.HerdrAddress{Session: "auth", ConfigRoot: herdrConfigRoot},
		Handle:  "auth", Proof: state.OwnershipProof{Scheme: namedSessionIntentProof, Data: proof},
		Owned: true, Phase: state.AttachmentActive,
	}
	runner := &closeableOwnershipRunner{replies: map[string][]string{
		shellcmd.LoginShell("herdr session list --json"): {`[{"name":"auth"}]`, `[]`},
		namedSessionCommand("stop", "auth"):              {""},
		namedSessionCommand("delete", "auth"):            {""},
	}}
	old := attachmentRemoteConnect
	attachmentRemoteConnect = func(context.Context, string, string) (closeableRemoteRunner, error) { return runner, nil }
	t.Cleanup(func() { attachmentRemoteConnect = old })
	if err := CloseSessionAttachment(context.Background(), state.Record{}, attachment); err != nil {
		t.Fatal(err)
	}
}

func TestCloseAttachmentObservesAbsence(t *testing.T) {
	attachment := ownedWorkspaceAttachment(t)
	runner := &ownershipRunner{replies: map[string][]string{
		"machine list --json":                    {machineReply()},
		"--machine mid workspace list":           {workspaceReply("w1", "auth"), `{"result":{"workspaces":[]}}`},
		"--machine mid pane list --workspace w1": {panesReply("term-owned")},
		"--machine mid workspace close w1":       {`{"result":{"type":"ok"}}`},
	}}
	withAttachmentRunner(t, runner)
	record := state.Record{Name: "instance", HerdrMachineID: "mid"}
	if err := CloseSessionAttachment(context.Background(), record, attachment); err != nil {
		t.Fatal(err)
	}
}

// Laying out tabs can close the root pane the proof was recorded from, so the
// proof has to follow the workspace or every later close would be refused.
func TestRefreshWorkspaceProofFollowsLayout(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	attachment := ownedWorkspaceAttachment(t)
	const generation = "00112233445566778899aabbccddeeff"
	record := state.Record{Name: "instance", Sessions: []state.Session{{
		Name: "auth", Generation: generation, AttachmentsKnown: true, Attachments: []state.HerdrAttachment{attachment},
	}}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	runner := &ownershipRunner{replies: map[string][]string{
		"--machine mid pane list --workspace w1": {`{"result":{"panes":[{"pane_id":"w1:p3","terminal_id":"term-new","workspace_id":"w1"}]}}`},
	}}
	withAttachmentRunner(t, runner)

	if err := verifyWorkspaceOwnership(runner, attachment); err == nil {
		t.Fatal("the original root pane is gone, so the old proof should no longer match")
	}
	refreshed, err := RefreshWorkspaceProof(context.Background(), store, "instance", generation, attachment)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkspaceOwnership(runner, refreshed); err != nil {
		t.Fatalf("refreshed proof rejected: %v", err)
	}
	got, _, _ := store.Get("instance")
	sess, _ := got.FindSession("auth")
	if persisted := decodeProof(t, sess.Attachments[0]); persisted != decodeProof(t, refreshed) {
		t.Errorf("the refreshed proof was not persisted: %+v", persisted)
	}
}

func TestRefreshWorkspaceProofKeepsTheOldProofWhenNothingIsLeft(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	attachment := ownedWorkspaceAttachment(t)
	const generation = "00112233445566778899aabbccddeeff"
	if err := store.Put(state.Record{Name: "instance", Sessions: []state.Session{{
		Name: "auth", Generation: generation, AttachmentsKnown: true, Attachments: []state.HerdrAttachment{attachment},
	}}}); err != nil {
		t.Fatal(err)
	}
	withAttachmentRunner(t, &ownershipRunner{replies: map[string][]string{
		"--machine mid pane list --workspace w1": {`{"result":{"panes":[]}}`},
	}})
	if _, err := RefreshWorkspaceProof(context.Background(), store, "instance", generation, attachment); err == nil {
		t.Fatal("refreshed a proof from a workspace with no panes")
	}
	got, _, _ := store.Get("instance")
	sess, _ := got.FindSession("auth")
	if kept := decodeProof(t, sess.Attachments[0]); kept != decodeProof(t, attachment) {
		t.Errorf("the proof was overwritten: %+v", kept)
	}
}

// decodeProof compares evidence, not bytes: the store re-indents JSON.
func decodeProof(t *testing.T, attachment state.HerdrAttachment) workspaceTerminalEvidence {
	t.Helper()
	var evidence workspaceTerminalEvidence
	if err := json.Unmarshal(attachment.Proof.Data, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}
