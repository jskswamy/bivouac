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
    "label": "cloudlab/status-reporting",
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
// a machine profile addresses one remote session, and two cloudlab sessions
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

// The session name is what the user thinks in, and the sidebar is narrow.
// Qualifying every label put the instance first and truncated to
// "jskswamy-cloudlab/su...", cutting the only part that identifies which
// session it is.
func TestMachineLabel_IsJustTheSessionName(t *testing.T) {
	if got := MachineLabel("auth"); got != "auth" {
		t.Errorf("MachineLabel() = %q, want the bare session name", got)
	}
}

// Two instances can each hold a session called "auth". The disambiguator is
// a suffix so that truncation eats the instance rather than the name.
func TestQualifiedMachineLabel_SuffixesTheInstance(t *testing.T) {
	got := qualifiedMachineLabel("jskswamy-cloudlab", "auth")
	if !strings.HasPrefix(got, "auth") {
		t.Errorf("qualifiedMachineLabel() = %q, want the session name first so a "+
			"truncated label still identifies the session", got)
	}
	if !strings.Contains(got, "jskswamy-cloudlab") {
		t.Errorf("qualifiedMachineLabel() = %q, want it to name the instance", got)
	}
	if got == qualifiedMachineLabel("ulai-ai-ulai", "auth") {
		t.Error("both instances produced the same qualified label")
	}
}

func TestMachineAddArgs_CarriesLabelAndRemoteSession(t *testing.T) {
	got := strings.Join(machineAddArgs("ssh://u@1.2.3.4", "inst/auth", "auth"), " ")

	for _, want := range []string{"machine", "add", "--label", "inst/auth", "--remote-session", "auth", "ssh://u@1.2.3.4"} {
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
	args := machineAddArgs("ssh://u@1.2.3.4", "inst/auth", "auth")
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

// The default session is herdr's own, and passing --remote-session for it
// would name a session that need not exist.
func TestMachineAddArgs_OmitsRemoteSessionWhenThereIsNoCloudlabSession(t *testing.T) {
	got := strings.Join(machineAddArgs("ssh://u@1.2.3.4", "inst", ""), " ")
	if strings.Contains(got, "--remote-session") {
		t.Errorf("machineAddArgs() = %q, want no --remote-session when the session is empty", got)
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

// The session's workspace is found by the label cloudlab gave it, because
// workspace ids are assigned by herdr and differ per instance -- w2 here is
// w5 somewhere else.
func TestFindWorkspace_MatchesTheLabelCloudlabSet(t *testing.T) {
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

// Every remote herdr command has to name the session, or it addresses the
// instance's default server instead of the one this machine profile targets.
func TestRemoteHerdrCmds_NameTheSession(t *testing.T) {
	for name, got := range map[string]string{
		"list":   workspaceListCmd("auth"),
		"create": workspaceCreateCmd("auth", "/home/u/sessions/auth/repo", "auth"),
		"focus":  workspaceFocusCmd("auth", "w2"),
	} {
		if !strings.Contains(got, "--session") {
			t.Errorf("%s = %q, want --session so it reaches the session's own server", name, got)
		}
	}
}

// The point of creating it: the workspace is rooted in the session's
// checkout, so attaching lands in the code rather than in $HOME.
func TestWorkspaceCreateCmd_RootsTheWorkspaceInTheCheckout(t *testing.T) {
	repo := "/home/devuser/sessions/auth/cloudlab"
	got := workspaceCreateCmd("auth", repo, "auth")
	if !strings.Contains(got, "--cwd") || !strings.Contains(got, repo) {
		t.Errorf("workspaceCreateCmd() = %q, want --cwd naming the session checkout", got)
	}
	if !strings.Contains(got, "--label") {
		t.Errorf("workspaceCreateCmd() = %q, want a label so it can be found again", got)
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

func TestEnsureMachine_AddsWithTheBareSessionNameAndReturnsItsID(t *testing.T) {
	h := &fakeHerdr{
		listJSON: "[]",
		afterAddJSON: `[{"id":"newid00","label":"auth","target":"ssh://u@1.2.3.4",` +
			`"session":"auth","enabled":true}]`,
	}

	id, label, err := EnsureMachine(h, "inst", "ssh://u@1.2.3.4", "auth", nil)
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if label != "auth" {
		t.Errorf("label = %q, want the bare session name", label)
	}
	if id != "newid00" {
		t.Errorf("id = %q, want the id read back from the listing -- teardown removes by "+
			"exactly this and nothing else", id)
	}
}

// Running cloudlab herdr twice must not leave two profiles for one session:
// machine add does not deduplicate, and nothing else would.
func TestEnsureMachine_IsIdempotentAndStillReportsTheID(t *testing.T) {
	h := &fakeHerdr{listJSON: `[{"id":"abc","label":"auth",` +
		`"target":"ssh://u@1.2.3.4","session":"auth","enabled":true}]`}

	id, _, err := EnsureMachine(h, "inst", "ssh://u@1.2.3.4", "auth", nil)
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if h.ran("add") {
		t.Error("machine add ran for a profile that already exists -- that is a duplicate")
	}
	// Recording on the reuse path is what stops a session attached before this
	// existed from leaking at teardown.
	if id != "abc" {
		t.Errorf("id = %q, want abc even though nothing was added", id)
	}
}

func TestEnsureMachine_EnablesADisabledProfileAndReportsTheID(t *testing.T) {
	h := &fakeHerdr{listJSON: `[{"id":"abc","label":"auth",` +
		`"target":"ssh://u@1.2.3.4","session":"auth","enabled":false}]`}

	id, _, err := EnsureMachine(h, "inst", "ssh://u@1.2.3.4", "auth", nil)
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if !h.ran("enable") {
		t.Error("want machine enable for a disabled profile")
	}
	if h.ran("add") {
		t.Error("machine add ran for a profile that exists but is disabled")
	}
	if id != "abc" {
		t.Errorf("id = %q, want abc", id)
	}
}

// Two instances holding a session of the same name: both labels gain the
// instance, so a bare name always means there is only one.
func TestEnsureMachine_QualifiesBothSidesOfANameClash(t *testing.T) {
	h := &fakeHerdr{
		listJSON: `[{"id":"other","label":"auth","target":"ssh://u@9.9.9.9",` +
			`"session":"auth","enabled":true}]`,
		afterAddJSON: `[{"id":"other","label":"auth","target":"ssh://u@9.9.9.9",` +
			`"session":"auth","enabled":true},` +
			`{"id":"newid00","label":"auth (inst-b)","target":"ssh://u@1.2.3.4",` +
			`"session":"auth","enabled":true}]`,
	}
	owned := OwnedMachines{"other": "inst-a"}

	_, label, err := EnsureMachine(h, "inst-b", "ssh://u@1.2.3.4", "auth", owned)
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if !strings.Contains(label, "inst-b") {
		t.Errorf("our label = %q, want it qualified by the instance", label)
	}
	renamed := false
	for _, c := range h.calls {
		if len(c) > 2 && c[1] == "rename" && c[2] == "other" {
			renamed = true
		}
	}
	if !renamed {
		t.Error("want the existing cloudlab profile renamed too -- otherwise a bare " +
			"\"auth\" silently means whichever was registered first")
	}
}

// A profile the user added by hand is not cloudlab's to rename, even when it
// takes the name cloudlab wanted. Ours is qualified; theirs is left alone.
func TestEnsureMachine_NeverRenamesAProfileItDidNotRegister(t *testing.T) {
	h := &fakeHerdr{
		listJSON: `[{"id":"theirs","label":"auth","target":"ssh://u@9.9.9.9",` +
			`"session":"auth","enabled":true}]`,
		afterAddJSON: `[{"id":"theirs","label":"auth","target":"ssh://u@9.9.9.9",` +
			`"session":"auth","enabled":true},` +
			`{"id":"newid00","label":"auth (inst-b)","target":"ssh://u@1.2.3.4",` +
			`"session":"auth","enabled":true}]`,
	}

	_, label, err := EnsureMachine(h, "inst-b", "ssh://u@1.2.3.4", "auth", OwnedMachines{})
	if err != nil {
		t.Fatalf("EnsureMachine() error = %v", err)
	}
	if !strings.Contains(label, "inst-b") {
		t.Errorf("our label = %q, want it qualified", label)
	}
	if h.ran("rename") {
		t.Error("renamed a profile cloudlab never registered")
	}
}

// A failure to add must surface. Reporting success would leave the user
// looking for a sidebar entry that was never created.
func TestEnsureMachine_ReportsAFailedAdd(t *testing.T) {
	h := &fakeHerdr{listJSON: "[]", failOn: "add"}
	if _, _, err := EnsureMachine(h, "inst", "ssh://u@1.2.3.4", "auth", nil); err == nil {
		t.Error("EnsureMachine() error = nil after machine add failed")
	}
}

// RemoveMachine is what stops profiles outliving the sessions they point
// at. It removes by recorded id, so it can only ever touch what cloudlab
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

// A session cloudlab never attached has no recorded id, and teardown must
// then do nothing at all -- not list, not guess, not match. That is what
// keeps a profile the user added by hand safe from a cloudlab delete.
func TestRemoveMachine_DoesNothingWithoutARecordedID(t *testing.T) {
	h := &fakeHerdr{}

	if err := RemoveMachine(h, ""); err != nil {
		t.Errorf("RemoveMachine(\"\") error = %v, want nil", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("ran %v with no recorded id -- teardown must not go looking", h.calls)
	}
}

// The shape `herdr workspace list` returns, captured from a live instance.
type fakeRemote struct {
	listJSON        string
	afterCreateJSON string
	created         bool
	cmds            []string
	failOn          string
}

func (f *fakeRemote) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.failOn != "" && strings.Contains(cmd, f.failOn) {
		return "boom", fmt.Errorf("remote herdr failed")
	}
	if strings.Contains(cmd, "create") {
		// A real herdr shows the new workspace in the next listing, and
		// EnsureWorkspace reads the id back from there rather than from the
		// create reply -- so the double has to do the same or it tests
		// nothing.
		f.created = true
		return `{"result":{"workspace":{"workspace_id":"w9"}}}`, nil
	}
	if strings.Contains(cmd, "list") {
		if f.created {
			return f.afterCreateJSON, nil
		}
		return f.listJSON, nil
	}
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

func TestEnsureWorkspace_CreatesOneRootedInTheCheckout(t *testing.T) {
	r := &fakeRemote{
		listJSON: `{"result":{"workspaces":[{"workspace_id":"w1","label":"~"}]}}`,
		afterCreateJSON: `{"result":{"workspaces":[` +
			`{"workspace_id":"w1","label":"~"},{"workspace_id":"w9","label":"auth"}]}}`,
	}

	id, err := EnsureWorkspace(r, "auth", "/home/u/sessions/auth/repo", "auth")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if id != "w9" {
		t.Errorf("id = %q, want w9 read back from the listing", id)
	}
	if !r.ran("create") || !r.ran("/home/u/sessions/auth/repo") {
		t.Errorf("want a create rooted at the checkout, got %v", r.cmds)
	}
}

// Reconnecting to a session that already has its workspace must reuse it,
// not stack up a new one every time.
func TestEnsureWorkspace_ReusesTheExistingOne(t *testing.T) {
	r := &fakeRemote{listJSON: `{"result":{"workspaces":[` +
		`{"workspace_id":"w1","label":"~"},{"workspace_id":"w2","label":"auth"}]}}`}

	id, err := EnsureWorkspace(r, "auth", "/home/u/sessions/auth/repo", "auth")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if id != "w2" {
		t.Errorf("id = %q, want the existing w2", id)
	}
	if r.ran("create") {
		t.Error("created a second workspace for a session that already had one")
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
	got := MachineTarget("subramk", "206.189.140.27")
	if got != MachineTarget("subramk", "206.189.140.27") {
		t.Error("MachineTarget() is not stable across calls")
	}
	if !strings.HasPrefix(got, "ssh://") || !strings.Contains(got, "subramk@206.189.140.27") {
		t.Errorf("MachineTarget() = %q, want an ssh:// target herdr accepts", got)
	}
}

// A named herdr session starts with its own default "~" workspace, and
// cloudlab then adds the checkout-rooted one -- so the sidebar showed two
// entries per session, one of them useless.
//
// The empty default is closed, and only in the run that created ours: a
// later reconnect must not go tidying workspaces the user has since made.
func TestEnsureWorkspace_ClosesHerdrsEmptyDefault(t *testing.T) {
	r := &fakeRemote{
		listJSON: `{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1}]}}`,
		afterCreateJSON: `{"result":{"workspaces":[` +
			`{"workspace_id":"w1","label":"~","pane_count":1},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}

	if _, err := EnsureWorkspace(r, "auth", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if !r.ran("close") || !r.ran("w1") {
		t.Errorf("want the empty ~ closed after creating ours, got %v", r.cmds)
	}
}

// Someone working in the default workspace must not have it closed from
// under them. More than one pane is the cheapest evidence it is in use.
func TestEnsureWorkspace_LeavesADefaultThatIsInUse(t *testing.T) {
	r := &fakeRemote{
		listJSON: `{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3}]}}`,
		afterCreateJSON: `{"result":{"workspaces":[` +
			`{"workspace_id":"w1","label":"~","pane_count":3},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}

	if _, err := EnsureWorkspace(r, "auth", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if r.ran("close") {
		t.Error("closed a default workspace that had panes in it")
	}
}

// Reconnecting finds our workspace already there and must change nothing.
func TestEnsureWorkspace_ReconnectDoesNotTidy(t *testing.T) {
	r := &fakeRemote{listJSON: `{"result":{"workspaces":[` +
		`{"workspace_id":"w1","label":"~","pane_count":1},` +
		`{"workspace_id":"w2","label":"auth","pane_count":1}]}}`}

	if _, err := EnsureWorkspace(r, "auth", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if r.ran("close") {
		t.Error("closed a workspace on a plain reconnect")
	}
}

// A session cloudlab never attached has no herdr session either: starting a
// cloudlab session does not create one, only attaching does. Without this,
// teardown tried to stop a server that never existed and told the user to
// go and remove it by hand.
func TestCleanupHerdr_TouchesNothingWhenCloudlabNeverAttached(t *testing.T) {
	r := &fakeRemote{}
	CleanupHerdr(context.Background(), "", "unattached", r)
	if len(r.cmds) != 0 {
		t.Errorf("ran %v against the instance for a session cloudlab never attached", r.cmds)
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
