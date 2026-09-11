package lifecycle

import (
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

// Two instances can each hold a session called "auth", and the label is the
// only thing telling them apart in the sidebar.
func TestMachineLabel_IsQualifiedByInstance(t *testing.T) {
	a := MachineLabel("jskswamy-cloudlab", "auth")
	b := MachineLabel("ulai-ai-ulai", "auth")
	if a == b {
		t.Fatalf("both instances produced %q -- the sidebar could not tell them apart", a)
	}
	if !strings.Contains(a, "auth") || !strings.Contains(a, "jskswamy-cloudlab") {
		t.Errorf("MachineLabel() = %q, want it to name both the instance and the session", a)
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
