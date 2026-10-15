package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The shape `herdr machine list --json` actually returns, captured from
// herdr 0.9.0 rather than written from the documentation.
const machineListJSON = `[
  {
    "id": "6410458fe77e5d1e3d98b7f040545c7b",
    "label": "bivouac/status-reporting",
    "target": "ssh://subramk@206.189.140.27",
    "session": "status-reporting",
    "enabled": true,
    "selected": false
  },
  {
    "id": "8ceb3b19d1134872ff17ab548635a6b9",
    "label": "ulai",
    "target": "subramk@64.227.190.118",
    "session": "default",
    "enabled": false,
    "selected": false
  }
]`

func TestParseMachineList_ReadsEveryFieldTheLifecycleNeeds(t *testing.T) {
	got, err := parseMachineList(machineListJSON)
	if err != nil {
		t.Fatalf("parseMachineList() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d profiles, want 2", len(got))
	}
	first := got[0]
	if first.ID != "6410458fe77e5d1e3d98b7f040545c7b" {
		t.Errorf("ID = %q", first.ID)
	}
	if first.Target != "ssh://subramk@206.189.140.27" || first.Session != "status-reporting" {
		t.Errorf("target/session = %q/%q", first.Target, first.Session)
	}
	if !first.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got[1].Enabled {
		t.Error("second profile Enabled = true, want false")
	}
}

// An empty catalog is the first-run case, not a failure.
func TestParseMachineList_EmptyCatalogIsNotAnError(t *testing.T) {
	got, err := parseMachineList("[]")
	if err != nil {
		t.Fatalf("parseMachineList(\"[]\") error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d profiles, want 0", len(got))
	}
}

// Matching is on target+session because that pair is what a profile *is*:
// a machine profile addresses one remote session, and two bivouac sessions
// on one instance are two profiles sharing a target. Labels are excluded
// deliberately -- the user may rename a profile, and herdr's own docs warn
// against deriving identity from anything but the id it hands back.
func TestFindMachine_MatchesOnTargetAndSessionNotLabel(t *testing.T) {
	profiles, err := parseMachineList(machineListJSON)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := findMachine(profiles, "ssh://subramk@206.189.140.27", "status-reporting")
	if !ok {
		t.Fatal("findMachine() found nothing for a target+session that is present")
	}
	if got.ID != "6410458fe77e5d1e3d98b7f040545c7b" {
		t.Errorf("ID = %q, want the status-reporting profile", got.ID)
	}

	// Same host, different session: a different profile, not this one.
	if _, ok := findMachine(profiles, "ssh://subramk@206.189.140.27", "other"); ok {
		t.Error("findMachine() matched a different session on the same host -- a profile " +
			"targets one remote session and must not be reused for another")
	}
	// Same session name, different host.
	if _, ok := findMachine(profiles, "ssh://subramk@10.0.0.1", "status-reporting"); ok {
		t.Error("findMachine() matched a different host")
	}
}

func TestMachineLabel_IsTheInstanceName(t *testing.T) {
	if got := MachineLabel("jskswamy-bivouac"); got != "jskswamy-bivouac" {
		t.Fatalf("MachineLabel() = %q, want the instance name", got)
	}
}

func TestMachineAddArgs_OmitsRemoteSessionForSharedMachine(t *testing.T) {
	got := strings.Join(machineAddArgs("ssh://u@1.2.3.4", "jskswamy-bivouac"), " ")
	if strings.Contains(got, "--remote-session") {
		t.Fatalf("machineAddArgs() = %q, want no --remote-session for a shared instance machine", got)
	}
	for _, want := range []string{"machine", "add", "ssh://u@1.2.3.4", "--label", "jskswamy-bivouac"} {
		if !strings.Contains(got, want) {
			t.Errorf("machineAddArgs() = %q, want it to contain %q", got, want)
		}
	}
}

// Order, not just presence. herdr's --help documents the target last
// ("[OPTIONS] --label <LABEL> <SSH_TARGET>") but the binary rejects that
// with a usage error and exit 2; the target has to come straight after the
// verb. Containment assertions passed happily while the real command could
// not run at all.
func TestMachineAddArgs_PutsTheTargetFirst(t *testing.T) {
	args := machineAddArgs("ssh://u@1.2.3.4", "jskswamy-bivouac")
	if len(args) < 3 {
		t.Fatalf("machineAddArgs() = %v, too short", args)
	}
	if args[0] != "machine" || args[1] != "add" {
		t.Fatalf("machineAddArgs() = %v, want it to start with machine add", args)
	}
	if args[2] != "ssh://u@1.2.3.4" {
		t.Errorf("args[2] = %q, want the ssh target -- herdr exits 2 when it comes after the flags", args[2])
	}
}

func TestMachineEnableAndRemoveArgs_AddressTheProfileByID(t *testing.T) {
	id := "6410458fe77e5d1e3d98b7f040545c7b"
	enable := strings.Join(machineEnableArgs(id), " ")
	remove := strings.Join(machineRemoveArgs(id), " ")

	if !strings.Contains(enable, "enable") || !strings.Contains(enable, id) {
		t.Errorf("machineEnableArgs() = %q", enable)
	}
	if !strings.Contains(remove, "remove") || !strings.Contains(remove, id) {
		t.Errorf("machineRemoveArgs() = %q", remove)
	}
}

// The shape `herdr workspace list` returns, captured from a live instance.
const workspaceListJSON = `{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[` +
	`{"active_tab_id":"w1:t1","focused":true,"label":"~","workspace_id":"w1"},` +
	`{"active_tab_id":"w2:t1","focused":false,"label":"auth","workspace_id":"w2"}]}}`

func TestParseWorkspaceList_ReadsIDsAndLabels(t *testing.T) {
	got, err := parseWorkspaceList(workspaceListJSON)
	if err != nil {
		t.Fatalf("parseWorkspaceList() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(got))
	}
	if got[1].ID != "w2" || got[1].Label != "auth" {
		t.Errorf("second workspace = %+v, want w2/auth", got[1])
	}
}

// The session's workspace is found by the label bivouac gave it, because
// workspace ids are assigned by herdr and differ per instance -- w2 here is
// w5 somewhere else.
func TestFindWorkspace_MatchesTheLabelBivouacSet(t *testing.T) {
	ws, err := parseWorkspaceList(workspaceListJSON)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findWorkspace(ws, "auth")
	if !ok || got.ID != "w2" {
		t.Errorf("findWorkspace(auth) = %+v, %v; want w2", got, ok)
	}
	if _, ok := findWorkspace(ws, "nope"); ok {
		t.Error("findWorkspace() matched a label that is not there")
	}
}

// fakeHerdr records every local herdr invocation and answers the listing
// from a script, so the whole ensure flow can be driven without herdr.
type fakeHerdr struct {
	listJSON     string
	afterAddJSON string
	added        bool
	calls        [][]string
	failOn       string // first arg to fail on; empty means never
}

func (f *fakeHerdr) Run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) > 1 && args[1] == f.failOn {
		return "boom", fmt.Errorf("herdr %s failed", args[1])
	}
	if len(args) > 1 && args[1] == "add" {
		// A real add shows up in the next listing, and EnsureMachine reads
		// the id back from there rather than scraping the add output.
		f.added = true
		return "Saved SSH machine newid00. Remote server is ready.", nil
	}
	if len(args) > 1 && args[1] == "list" {
		if f.added && f.afterAddJSON != "" {
			return f.afterAddJSON, nil
		}
		return f.listJSON, nil
	}
	return "", nil
}

func (f *fakeHerdr) ran(verb string) bool {
	for _, c := range f.calls {
		if len(c) > 1 && c[1] == verb {
			return true
		}
	}
	return false
}

func TestEnsureMachine_AddsWithInstanceLabelWhenNoRecordedID(t *testing.T) {
	h := &fakeHerdr{
		listJSON: "[]",
		afterAddJSON: `[{"id":"newid00","label":"jskswamy-bivouac","target":"ssh://u@1.2.3.4",` +
			`"session":"default","enabled":true}]`,
	}

	id, label, err := EnsureMachine(h, "jskswamy-bivouac", "ssh://u@1.2.3.4", "")
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if label != "jskswamy-bivouac" {
		t.Errorf("label = %q, want the instance name", label)
	}
	if id != "newid00" {
		t.Errorf("id = %q, want the id read back from the listing", id)
	}
}

func TestEnsureMachine_ReusesUnrecordedInstanceMachineByTargetAndLabel(t *testing.T) {
	h := &fakeHerdr{listJSON: `[{"id":"mid","label":"jskswamy-bivouac",` +
		`"target":"ssh://u@1.2.3.4","session":"default","enabled":true}]`}

	id, label, err := EnsureMachine(h, "jskswamy-bivouac", "ssh://u@1.2.3.4", "")
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if h.ran("add") {
		t.Error("machine add ran for an instance machine that already exists")
	}
	if id != "mid" || label != "jskswamy-bivouac" {
		t.Errorf("EnsureMachine() = %q, %q; want mid/jskswamy-bivouac", id, label)
	}
}

func TestEnsureMachine_ReusesRecordedInstanceMachineID(t *testing.T) {
	h := &fakeHerdr{listJSON: `[{"id":"mid","label":"renamed by user",` +
		`"target":"ssh://u@1.2.3.4","session":"default","enabled":true}]`}

	id, label, err := EnsureMachine(h, "jskswamy-bivouac", "ssh://u@1.2.3.4", "mid")
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if h.ran("add") {
		t.Error("machine add ran for a recorded profile that already exists")
	}
	if id != "mid" {
		t.Errorf("id = %q, want mid", id)
	}
	if label != "renamed by user" {
		t.Errorf("label = %q, want the user's current label", label)
	}
}

func TestEnsureMachine_EnablesADisabledRecordedProfileAndReportsTheID(t *testing.T) {
	h := &fakeHerdr{listJSON: `[{"id":"mid","label":"jskswamy-bivouac",` +
		`"target":"ssh://u@1.2.3.4","session":"default","enabled":false}]`}

	id, _, err := EnsureMachine(h, "jskswamy-bivouac", "ssh://u@1.2.3.4", "mid")
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if !h.ran("enable") {
		t.Error("want machine enable for a disabled recorded profile")
	}
	if h.ran("add") {
		t.Error("machine add ran for a profile that exists but is disabled")
	}
	if id != "mid" {
		t.Errorf("id = %q, want mid", id)
	}
}

// A failure to add must surface. Reporting success would leave the user
// looking for a sidebar entry that was never created.
func TestEnsureMachine_ReportsAFailedAdd(t *testing.T) {
	h := &fakeHerdr{listJSON: "[]", failOn: "add"}
	if _, _, err := EnsureMachine(h, "inst", "ssh://u@1.2.3.4", ""); err == nil {
		t.Error("EnsureMachine() error = nil after machine add failed")
	}
}

// RemoveMachine is what stops profiles outliving the sessions they point
// at. It removes by recorded id, so it can only ever touch what bivouac
// registered.
func TestRemoveMachine_RemovesTheRecordedProfileOnly(t *testing.T) {
	h := &fakeHerdr{}

	if err := RemoveMachine(h, "drop"); err != nil {
		t.Fatalf("RemoveMachine() error = %v", err)
	}
	var removed []string
	for _, c := range h.calls {
		if len(c) > 2 && c[1] == "remove" {
			removed = append(removed, c[2])
		}
	}
	if len(removed) != 1 || removed[0] != "drop" {
		t.Errorf("removed %v, want exactly [drop]", removed)
	}
}

// A session bivouac never attached has no recorded id, and teardown must
// then do nothing at all -- not list, not guess, not match. That is what
// keeps a profile the user added by hand safe from a bivouac delete.
func TestRemoveMachine_DoesNothingWithoutARecordedID(t *testing.T) {
	h := &fakeHerdr{}

	if err := RemoveMachine(h, ""); err != nil {
		t.Errorf("RemoveMachine(\"\") error = %v, want nil", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("ran %v with no recorded id -- teardown must not go looking", h.calls)
	}
}

// The guard used to refuse outright. Inside herdr is now the case the
// machine path serves, and outside it there is no window to attach to, so
// launching a client stays right.
func TestInsideHerdr_RoutesRatherThanRefuses(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	if !InsideHerdr() {
		t.Error("InsideHerdr() = false with HERDR_ENV set")
	}
	t.Setenv("HERDR_ENV", "")
	if InsideHerdr() {
		t.Error("InsideHerdr() = true with HERDR_ENV empty")
	}
}

// The target has to be the one a machine profile is matched on, so the same
// instance produces the same string every time -- otherwise every run looks
// like a new machine and adds a duplicate.
func TestMachineTarget_IsStableAndSSHShaped(t *testing.T) {
	got, err := MachineTarget(context.Background(), "206.189.140.27", "subramk")
	if err != nil {
		t.Fatal(err)
	}
	again, err := MachineTarget(context.Background(), "206.189.140.27", "subramk")
	if err != nil {
		t.Fatal(err)
	}
	if got != again {
		t.Error("MachineTarget() is not stable across calls")
	}
	if !strings.HasPrefix(got, "ssh://") || !strings.Contains(got, "subramk@206.189.140.27") {
		t.Errorf("MachineTarget() = %q, want an ssh:// target herdr accepts", got)
	}
}

// fakeRemote records the commands CleanupHerdr runs on the instance.
type fakeRemote struct {
	cmds []string
}

func (f *fakeRemote) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return "", nil
}

func (f *fakeRemote) ran(fragment string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, fragment) {
			return true
		}
	}
	return false
}

// A session bivouac never attached has no herdr session either: starting a
// bivouac session does not create one, only attaching does. Without this,
// teardown tried to stop a server that never existed and told the user to
// go and remove it by hand.
func TestCleanupHerdr_TouchesNothingWhenBivouacNeverAttached(t *testing.T) {
	r := &fakeRemote{}
	CleanupHerdr(context.Background(), "", "unattached", r)
	if len(r.cmds) != 0 {
		t.Errorf("ran %v against the instance for a session bivouac never attached", r.cmds)
	}
}

// With a recorded id, both halves run: the profile is forgotten here and the
// session server is stopped and deleted there.
func TestCleanupHerdr_StopsAndDeletesTheRemoteSession(t *testing.T) {
	r := &fakeRemote{}
	CleanupHerdr(context.Background(), "someid", "auth", r)
	if !r.ran("session stop") || !r.ran("session delete") {
		t.Errorf("want the remote session stopped and deleted, got %v", r.cmds)
	}
}

func TestCleanupHerdr_RemovesLegacySessionMachine(t *testing.T) {
	var removed []string
	old := removeHerdrMachine
	removeHerdrMachine = func(_ herdrRunner, id string) error {
		removed = append(removed, id)
		return nil
	}
	defer func() { removeHerdrMachine = old }()

	r := &fakeRemote{}
	CleanupHerdr(context.Background(), "legacy", "auth", r)
	if len(removed) != 1 || removed[0] != "legacy" {
		t.Fatalf("removed %v, want [legacy]", removed)
	}
	if !r.ran("session stop") || !r.ran("session delete") {
		t.Errorf("want the remote session stopped and deleted, got %v", r.cmds)
	}
}

func TestDeleteSession_DoesNotRemoveSharedHerdrMachine(t *testing.T) {
	var removed []string
	old := removeHerdrMachine
	removeHerdrMachine = func(_ herdrRunner, id string) error {
		removed = append(removed, id)
		return nil
	}
	defer func() { removeHerdrMachine = old }()

	CleanupHerdr(context.Background(), "", "auth", &fakeRemote{})
	if len(removed) != 0 {
		t.Fatalf("removed %v with no legacy session machine id", removed)
	}
}
