# herdr `--machine` Workspace and Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `bivouac herdr` (inside herdr) reaches the session's workspace through `herdr --machine` instead of a fresh SSH connection, and lays out user-declared tabs and panes (`herdrTabs`) inside it.

**Architecture:** Every workspace/tab/pane command becomes an argv routed through the existing `herdrRunner` seam (`internal/lifecycle/herdrprofile.go`) with a `--machine <profile-id>` prefix. The SSH-exec'd workspace path is deleted rather than kept alongside. `herdrTabs` is a new additive `Listing` in `Config.pkl`, merged base-first like `flakes`, read best-effort at attach time from the session's local `bivouac.pkl`.

**Tech Stack:** Go 1.26, Pkl 0.32 with pkl-go 0.14 codegen, herdr 0.9.1 CLI.

**Spec:** `docs/superpowers/specs/2026-09-18-herdr-machine-workspace-design.md` (read it first). Builds on `docs/superpowers/specs/2026-09-10-herdr-machines-design.md`.

## Global Constraints

- Work inside `nix develop` (Go, pkl, golangci-lint come from the flake). Run commands as `nix develop --command <cmd>` if the shell is not already in it.
- **Never pipe `go test`** into `tail`/`grep`/`tee` — it hangs (see `AGENTS.md`). Redirect: `nix develop --command go test ./internal/lifecycle/ > /tmp/t.log 2>&1; echo exit=$?` then read the file.
- Commit messages: imperative subject ≤ 50 chars, capitalised, no trailing period, no `type:` prefix; body wrapped at 72 explaining *why*. **No `Co-Authored-By` trailer of any kind** (AGENTS.md overrides any other attribution instruction).
- Comments explain *why*, at the density of `internal/lifecycle/herdrprofile.go`.
- Shell out to herdr via argv (`exec.Command`), never a shell string.
- Never combine `--machine` with `--session` or `--remote` (herdr's own docs).
- Do not touch `EnsureMachine`, `CleanupHerdr`'s session stop/delete, or the outside-herdr `Herdr()` / `herdrArgs` path.
- Leave no scratch files in the working tree; `.gocache/` is gitignored.

## Where this plan deliberately departs from the spec

Verified live against herdr 0.9.1 on this instance while planning; the spec is amended in Task 5 to match.

1. **`tab create` does take `--label`** (`herdr tab create --workspace W --label L --cwd P --no-focus`). Only panes need a `pane rename` after creation (`pane split` has no label flag).
2. **The capability gate cannot be `herdr --machine X status`**: herdr refuses it with `status --json is not an API-backed machine command` (exit 2). Forwarding "never installs, starts, or restarts a server and never falls back to Local", so the first routed call (`--machine X workspace list`) *is* the safe probe; its failure is reported with an instruction to `bivouac provision`.
3. **The `--machine` selector is the profile id**, not the label. herdr accepts either, but a label must be unique and the user may rename it; the id is what `EnsureMachine` returns and bivouac records.
4. **The SSH path already parsed JSON** (`parseWorkspaceList`), so it is reused unchanged — no new parser for workspaces.
5. **Names:** with the SSH path deleted there is nothing to disambiguate from, so the functions keep the names `EnsureWorkspace` / `FocusWorkspace` with a `herdrRunner` + machine signature rather than gaining a `ViaMachine` suffix.
6. **Tab/pane layout failures warn rather than fail the attach.** The machine is registered and the workspace exists by then; a missing tab is cosmetic.

## Captured herdr 0.9.1 payloads (use verbatim in tests)

```
workspace list  {"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[{"active_tab_id":"w1:t1","focused":true,"label":"~","workspace_id":"w1"},{"active_tab_id":"w2:t1","focused":false,"label":"auth","workspace_id":"w2"}]}}
tab list        {"id":"cli:tab:list","result":{"tabs":[{"agent_status":"unknown","focused":false,"label":"1","number":1,"pane_count":1,"tab_id":"w3:t1","workspace_id":"w3"},{"agent_status":"unknown","focused":false,"label":"run","number":2,"pane_count":2,"tab_id":"w3:t2","workspace_id":"w3"}],"type":"tab_list"}}
tab create      {"id":"cli:tab:create","result":{"root_pane":{"cwd":"/tmp","pane_id":"w3:p2","tab_id":"w3:t2","workspace_id":"w3"},"tab":{"label":"run","number":2,"pane_count":1,"tab_id":"w3:t2","workspace_id":"w3"},"type":"tab_created"}}
pane split      {"id":"cli:pane:split","result":{"pane":{"cwd":"/var","pane_id":"w3:p3","tab_id":"w3:t2","workspace_id":"w3"},"type":"pane_info"}}
pane list       {"id":"cli:pane:list","result":{"panes":[{"cwd":"/tmp","pane_id":"w3:p1","tab_id":"w3:t1","workspace_id":"w3"},{"cwd":"/tmp","label":"server","pane_id":"w3:p2","tab_id":"w3:t2","workspace_id":"w3"},{"cwd":"/var","label":"logs","pane_id":"w3:p3","tab_id":"w3:t2","workspace_id":"w3"}],"type":"pane_list"}}
pane rename     {"id":"cli:pane:rename","result":{"pane":{"label":"logs","pane_id":"w3:p3","tab_id":"w3:t2"},"type":"pane_info"}}
pane run        (empty stdout on success)
```

A pane with no label omits the `label` key entirely. `workspace create` opens a default tab labelled `"1"` with one root pane; configured tabs are added *after* it and it is left alone.

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/Config.pkl` (modify) | `herdrTabs` schema + `HerdrTab`/`HerdrPane` classes |
| `internal/config/Config.pkl.go`, `init.pkl.go` (regenerated), `HerdrTab.pkl.go`, `HerdrPane.pkl.go` (generated, new) | pkl-go bindings |
| `internal/config/config.go` (modify) | `mergeConfig` carries `HerdrTabs` base-first |
| `internal/config/config_test.go` (modify) | merge + Resolve-level tests |
| `internal/lifecycle/herdrworkspace.go` (rewrite) | `--machine`-routed workspace list/create/focus/close |
| `internal/lifecycle/herdrworkspace_test.go` (new) | `fakeRouted` double + workspace tests |
| `internal/lifecycle/herdrtabs.go` (new) | `EnsureTabs`: tab/pane layout over `--machine` |
| `internal/lifecycle/herdrtabs_test.go` (new) | layout tests |
| `internal/lifecycle/herdrattach.go` (modify) | `AttachMachine` drops SSH, adds tabs; `remoteRunner` moves here |
| `internal/lifecycle/herdrmachine_test.go` (modify) | drop SSH workspace tests, simplify `fakeRemote` |
| `cmd/run_attach.go` (modify) | load `herdrTabs`, pass through, record id even on error |
| `docs/config.md`, the 09-18 spec (modify) | document `herdrTabs`; record the departures above |

---

### Task 1: `herdrTabs` config schema

**Files:**
- Modify: `internal/config/Config.pkl` (append after `flakes` / `class Flake`)
- Regenerate: `internal/config/Config.pkl.go`, `internal/config/init.pkl.go`; new `internal/config/HerdrTab.pkl.go`, `internal/config/HerdrPane.pkl.go`
- Modify: `internal/config/config.go` (`mergeConfig`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.HerdrTab{Label string; Panes []HerdrPane}`, `config.HerdrPane{Label string; Command *string; Cwd *string}`, field `config.Config.HerdrTabs []HerdrTab` (so `Resolved.HerdrTabs` via embedding).

- [ ] **Step 1: Write the failing tests** — append to `internal/config/config_test.go`:

```go
func TestMergeConfig_HerdrTabsAreAdditiveBaseFirst(t *testing.T) {
	base := Config{HerdrTabs: []HerdrTab{{Label: "agent"}}}
	project := Config{HerdrTabs: []HerdrTab{{Label: "run"}}}

	got := mergeConfig(base, project).HerdrTabs
	if len(got) != 2 || got[0].Label != "agent" || got[1].Label != "run" {
		t.Errorf("HerdrTabs = %+v, want [agent run] -- base's tabs are created first", got)
	}
}

// Through pkl, not just mergeConfig: the classes have to be reachable from
// an amending file by their bare names, and the optional pane fields have
// to come back as nil rather than "".
func TestResolve_HerdrTabsMergeAcrossBaseAndProject(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pkl")
	writeFixture(t, base, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`herdrTabs {`,
		`  new HerdrTab {`,
		`    label = "agent"`,
		`  }`,
		`}`,
	}, "\n")+"\n")
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`template = "python"`,
		`herdrTabs {`,
		`  new HerdrTab {`,
		`    label = "run"`,
		`    panes {`,
		`      new HerdrPane {`,
		`        label = "server"`,
		`        command = "npm run dev"`,
		`      }`,
		`      new HerdrPane {`,
		`        label = "logs"`,
		`        cwd = "logs"`,
		`      }`,
		`    }`,
		`  }`,
		`}`,
	}, "\n")+"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.HerdrTabs) != 2 || cfg.HerdrTabs[0].Label != "agent" || cfg.HerdrTabs[1].Label != "run" {
		t.Fatalf("HerdrTabs = %+v, want [agent run]", cfg.HerdrTabs)
	}
	panes := cfg.HerdrTabs[1].Panes
	if len(panes) != 2 {
		t.Fatalf("run panes = %+v, want 2", panes)
	}
	if panes[0].Command == nil || *panes[0].Command != "npm run dev" || panes[0].Cwd != nil {
		t.Errorf("server pane = %+v, want command set and cwd nil", panes[0])
	}
	if panes[1].Cwd == nil || *panes[1].Cwd != "logs" || panes[1].Command != nil {
		t.Errorf("logs pane = %+v, want cwd set and command nil", panes[1])
	}
}

func TestResolve_HerdrTabsDefaultToEmpty(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, project, "region = \"nyc3\"\nsize = \"s\"\ntemplate = \"python\"\nbasePath = \"./none.pkl\"\n")

	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(cfg.HerdrTabs) != 0 {
		t.Errorf("HerdrTabs = %+v, want none -- layout is opt-in", cfg.HerdrTabs)
	}
}
```

Check `writeFixture`'s signature and how the existing Resolve tests avoid the developer's real `~/.config/bivouac/base.pkl` (look at `TestLoad_MergesWithBase_ScalarsOverrideListsAdditive` and `main_test.go`); follow the same isolation. If `basePath = "./none.pkl"` is not how other tests say "no base", copy whatever they do instead.

- [ ] **Step 2: Run to verify failure**

Run: `nix develop --command go test ./internal/config/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: build failure — `undefined: HerdrTab`, `cfg.HerdrTabs undefined`.

- [ ] **Step 3: Add the schema** — append to `internal/config/Config.pkl` after `class Flake { ... }`:

```pkl
/// Tabs, and the panes inside them, that `bivouac herdr` lays out in a
/// session's workspace when it attaches from inside herdr.
///
/// Opt-in and additive like `packages`: base's tabs are created before
/// this file's. bivouac declares no layout of its own, because what a
/// useful tab looks like depends on the project's workflow.
///
/// Matched by label on every attach, so re-attaching adds only what is
/// missing and never runs a pane's command twice.
herdrTabs: Listing<HerdrTab> = new Listing {}

class HerdrTab {
  label: String
  /// In declaration order: the first is the tab's own pane, and each
  /// later one splits off the one before it.
  panes: Listing<HerdrPane> = new Listing {}
}

class HerdrPane {
  label: String
  /// Run once, when the pane is first created.
  command: String?
  /// Relative to the session's checkout on the instance; omitted means
  /// the checkout itself.
  cwd: String?
}
```

- [ ] **Step 4: Regenerate the bindings**

Run: `nix develop --command go generate ./internal/config/`
Expected: `Config.pkl.go` gains `HerdrTabs []HerdrTab \`pkl:"herdrTabs"\`` (with the doc comment), new `HerdrTab.pkl.go` / `HerdrPane.pkl.go`, and `init.pkl.go` registers `bivouac.Config#HerdrTab` and `bivouac.Config#HerdrPane`.

If generation cannot fetch the pkl-go package (no network), write the files by hand in exactly the generator's format, matching `Flake.pkl.go` and `init.pkl.go`:

```go
// Code generated from Pkl module `bivouac.Config`. DO NOT EDIT.
package config

type HerdrTab struct {
	Label string `pkl:"label"`

	// In declaration order: the first is the tab's own pane, and each
	// later one splits off the one before it.
	Panes []HerdrPane `pkl:"panes"`
}
```

```go
// Code generated from Pkl module `bivouac.Config`. DO NOT EDIT.
package config

type HerdrPane struct {
	Label string `pkl:"label"`

	// Run once, when the pane is first created.
	Command *string `pkl:"command"`

	// Relative to the session's checkout on the instance; omitted means
	// the checkout itself.
	Cwd *string `pkl:"cwd"`
}
```

plus the `HerdrTabs` field (with the Pkl doc comment turned into `//` lines) at the end of `Config`, and two `pkl.RegisterMappingFor` lines in `init.pkl.go`.

- [ ] **Step 5: Merge it** — in `internal/config/config.go` `mergeConfig`, add after `Flakes:`:

```go
		HerdrTabs: append(append([]HerdrTab{}, base.HerdrTabs...), project.HerdrTabs...),
```

(gofmt will realign the struct literal.)

- [ ] **Step 6: Run the config package tests**

Run: `nix develop --command go test ./internal/config/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: exit=0. If a schema-drift test (e.g. one comparing `fieldDefaults`/`Fields()` to the schema) fails, do **not** add `herdrTabs` to the wizard's `Values` machinery (`write.go`/`read.go`) — it is not a wizard field. Instead make the drift test ignore it in the narrowest way and explain why in a comment. Report this in your summary if it happens.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "Add herdrTabs to the config schema" -m "Tabs and panes for bivouac herdr to lay out in a session's workspace.
Opt-in and merged base-first like flakes: layout is workflow-specific,
so bivouac declares none of it itself."
```

---

### Task 2: Route workspace list/create/focus through `--machine`

**Files:**
- Rewrite: `internal/lifecycle/herdrworkspace.go`
- Modify: `internal/lifecycle/herdrattach.go` (`AttachMachine`; receive `remoteRunner`)
- Create: `internal/lifecycle/herdrworkspace_test.go`
- Modify: `internal/lifecycle/herdrmachine_test.go`

**Interfaces:**
- Consumes: `herdrRunner` (`Run(args ...string) (string, error)`) and `localHerdr` — unchanged.
- Produces (later tasks rely on these exact names):
  - `func machineArgs(machine string, args ...string) []string`
  - `func EnsureWorkspace(h herdrRunner, machine, cwd, label string) (string, error)`
  - `func FocusWorkspace(h herdrRunner, machine, id string) error`
  - test double `fakeRouted` with `replies map[string][]string`, `fail string`, `calls [][]string`, `machines []string`, method `ran(verb string) [][]string`
  - `AttachMachine` keeps its current signature in this task.

- [ ] **Step 1: Write the failing tests** — create `internal/lifecycle/herdrworkspace_test.go`:

```go
package lifecycle

import (
	"fmt"
	"strings"
	"testing"
)

// fakeRouted answers herdr commands routed to a saved machine, so the
// workspace and layout flows can run without herdr.
//
// Replies are queued per verb ("workspace list", "pane split") and the last
// one repeats, which is how a listing can differ before and after a create.
type fakeRouted struct {
	replies  map[string][]string
	fail     string     // verb to fail, e.g. "pane split"; empty means never
	calls    [][]string // argv after the --machine prefix
	machines []string   // the selector each call was routed to
}

func (f *fakeRouted) Run(args ...string) (string, error) {
	if len(args) < 4 || args[0] != "--machine" {
		return "", fmt.Errorf("not routed through --machine: %v", args)
	}
	f.machines = append(f.machines, args[1])
	rest := args[2:]
	f.calls = append(f.calls, rest)
	verb := rest[0] + " " + rest[1]
	if verb == f.fail {
		return "boom", fmt.Errorf("herdr %s failed", verb)
	}
	queue := f.replies[verb]
	if len(queue) == 0 {
		return "", nil
	}
	if len(queue) > 1 {
		f.replies[verb] = queue[1:]
	}
	return queue[0], nil
}

// ran returns every call of verb, each without the verb itself.
func (f *fakeRouted) ran(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0]+" "+c[1] == verb {
			out = append(out, c[2:])
		}
	}
	return out
}

// Every routed command names the machine and never a session: herdr's docs
// forbid combining them, since the profile already carries its session.
func TestWorkspaceArgs_RouteThroughTheMachineNotASession(t *testing.T) {
	for name, got := range map[string][]string{
		"list":   workspaceListArgs("mid"),
		"create": workspaceCreateArgs("mid", "/home/u/sessions/auth/repo", "auth"),
		"focus":  workspaceFocusArgs("mid", "w2"),
		"close":  workspaceCloseArgs("mid", "w1"),
	} {
		if len(got) < 2 || got[0] != "--machine" || got[1] != "mid" {
			t.Errorf("%s = %v, want it to start with --machine mid", name, got)
		}
		if strings.Contains(strings.Join(got, " "), "--session") {
			t.Errorf("%s = %v, must not combine --machine with --session", name, got)
		}
	}
}

func TestEnsureWorkspace_CreatesOneRootedInTheCheckout(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3}]}}`,
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}}}

	id, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if id != "w9" {
		t.Errorf("id = %q, want w9 read back from the listing", id)
	}
	created := h.ran("workspace create")
	if len(created) != 1 {
		t.Fatalf("created %v, want exactly one workspace", created)
	}
	got := strings.Join(created[0], " ")
	for _, want := range []string{"--cwd /home/u/sessions/auth/repo", "--label auth", "--no-focus"} {
		if !strings.Contains(got, want) {
			t.Errorf("create = %q, want %q", got, want)
		}
	}
	for _, m := range h.machines {
		if m != "mid" {
			t.Errorf("routed to %q, want every call on the session's machine", m)
		}
	}
}

// Reconnecting to a session that already has its workspace must reuse it,
// not stack up a new one every time.
func TestEnsureWorkspace_ReusesTheExistingOne(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1},` +
			`{"workspace_id":"w2","label":"auth","pane_count":1}]}}`,
	}}}

	id, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth")
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if id != "w2" {
		t.Errorf("id = %q, want the existing w2", id)
	}
	if len(h.ran("workspace create")) != 0 {
		t.Error("created a second workspace for a session that already had one")
	}
	// Nor tidy: a reconnect must not close workspaces the user has since made.
	if len(h.ran("workspace close")) != 0 {
		t.Error("closed a workspace on a plain reconnect")
	}
}

// A named session starts with herdr's own "~" workspace; once ours exists
// the empty default is closed, in the run that created ours only.
func TestEnsureWorkspace_ClosesHerdrsEmptyDefault(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1}]}}`,
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":1},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}}}

	if _, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	closed := h.ran("workspace close")
	if len(closed) != 1 || closed[0][0] != "w1" {
		t.Errorf("closed %v, want exactly the empty ~ (w1)", closed)
	}
}

// More than one pane is the cheapest evidence someone is working in it.
func TestEnsureWorkspace_LeavesADefaultThatIsInUse(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{"workspace list": {
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3}]}}`,
		`{"result":{"workspaces":[{"workspace_id":"w1","label":"~","pane_count":3},` +
			`{"workspace_id":"w9","label":"auth","pane_count":1}]}}`,
	}}}

	if _, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth"); err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if len(h.ran("workspace close")) != 0 {
		t.Error("closed a default workspace that had panes in it")
	}
}

// The first routed call is the capability probe: forwarding never falls
// back to a local server, so a failure here means the instance's herdr
// cannot be driven this way, and the fix is to provision it.
func TestEnsureWorkspace_NamesProvisionWhenTheMachineCannotBeDriven(t *testing.T) {
	h := &fakeRouted{fail: "workspace list"}

	_, err := EnsureWorkspace(h, "mid", "/home/u/sessions/auth/repo", "auth")
	if err == nil {
		t.Fatal("EnsureWorkspace() error = nil when the machine could not be reached")
	}
	if !strings.Contains(err.Error(), "bivouac provision") {
		t.Errorf("error = %q, want it to name bivouac provision", err)
	}
	if len(h.ran("workspace create")) != 0 {
		t.Error("went on to create after the probe failed")
	}
}

func TestFocusWorkspace_FocusesByIDThroughTheMachine(t *testing.T) {
	h := &fakeRouted{}
	if err := FocusWorkspace(h, "mid", "w9"); err != nil {
		t.Fatalf("FocusWorkspace() error = %v", err)
	}
	focused := h.ran("workspace focus")
	if len(focused) != 1 || focused[0][0] != "w9" {
		t.Errorf("focused %v, want w9", focused)
	}
}

func TestFocusWorkspace_ReportsAFailure(t *testing.T) {
	h := &fakeRouted{fail: "workspace focus"}
	if err := FocusWorkspace(h, "mid", "w9"); err == nil {
		t.Error("FocusWorkspace() error = nil after focus failed")
	}
}
```

Then, in `internal/lifecycle/herdrmachine_test.go`, **delete** these now-obsolete SSH-path tests: `TestRemoteHerdrCmds_NameTheSession`, `TestWorkspaceCreateCmd_RootsTheWorkspaceInTheCheckout`, `TestEnsureWorkspace_CreatesOneRootedInTheCheckout`, `TestEnsureWorkspace_ReusesTheExistingOne`, `TestEnsureWorkspace_ClosesHerdrsEmptyDefault`, `TestEnsureWorkspace_LeavesADefaultThatIsInUse`, `TestEnsureWorkspace_ReconnectDoesNotTidy`. Keep `TestParseWorkspaceList_ReadsIDsAndLabels` and `TestFindWorkspace_MatchesTheLabelBivouacSet`. Replace `fakeRemote` (only the `CleanupHerdr` tests use it now) with the minimal version, fixing its stale lead comment:

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `nix develop --command go test ./internal/lifecycle/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: build failure — `undefined: workspaceListArgs` etc., and `EnsureWorkspace` argument type mismatch.

- [ ] **Step 3: Rewrite `internal/lifecycle/herdrworkspace.go`** — keep `herdrWorkspace`, `parseWorkspaceList`, `findWorkspace`, `herdrDefaultWorkspaceLabel` and their comments unchanged; delete `remoteHerdrCmd`, `workspaceListCmd`, `workspaceCreateCmd`, `workspaceFocusCmd`, `workspaceCloseCmd`, the old `EnsureWorkspace`/`FocusWorkspace`, and the `shellcmd` import. Move the `remoteRunner` interface (with its comment) to `herdrattach.go` just above `CleanupHerdr`, its only remaining user. Add:

```go
// machineArgs routes a herdr command to a saved machine's server.
//
// herdr 0.9.1's --machine reaches the instance through the profile
// EnsureMachine already saved, so these commands need no SSH connection of
// bivouac's own. The selector is the profile id: herdr also accepts a
// label, but only a unique one, and the user may rename it from the
// sidebar.
//
// Never alongside --session. The profile already carries its session, and
// herdr's docs rule the combination out.
func machineArgs(machine string, args ...string) []string {
	return append([]string{"--machine", machine}, args...)
}

func workspaceListArgs(machine string) []string {
	return machineArgs(machine, "workspace", "list")
}

// workspaceCreateArgs roots a workspace in the session's checkout, which is
// the whole point: attaching then lands in the code rather than in $HOME.
//
// --no-focus because focusing is a separate, later step -- creating a
// workspace should not move anyone who happens to be attached already.
func workspaceCreateArgs(machine, cwd, label string) []string {
	return machineArgs(machine, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus")
}

func workspaceFocusArgs(machine, id string) []string {
	return machineArgs(machine, "workspace", "focus", id)
}

func workspaceCloseArgs(machine, id string) []string {
	return machineArgs(machine, "workspace", "close", id)
}

// EnsureWorkspace makes sure the session has a workspace on the instance
// rooted in its checkout, and returns that workspace's id.
//
// The listing is also the capability probe. `herdr --machine` refuses
// `status`, so there is no cheaper question to ask first -- and forwarding
// never installs, restarts or falls back to a local server, so asking with
// a real command is safe. A failure here means the instance's herdr cannot
// be driven this way, which provisioning fixes.
//
// Reused when it already exists. Reconnecting to a session is the common
// case, and creating unconditionally would stack up a workspace per attach.
func EnsureWorkspace(h herdrRunner, machine, cwd, label string) (string, error) {
	out, err := h.Run(workspaceListArgs(machine)...)
	if err != nil {
		return "", fmt.Errorf("reaching the instance's herdr through saved machine %s: %w\n%s\n"+
			"its herdr may predate machine forwarding (0.9.1) -- run `bivouac provision`, then try again",
			machine, err, out)
	}
	workspaces, err := parseWorkspaceList(out)
	if err != nil {
		return "", err
	}
	if existing, found := findWorkspace(workspaces, label); found {
		return existing.ID, nil
	}

	if out, err := h.Run(workspaceCreateArgs(machine, cwd, label)...); err != nil {
		return "", fmt.Errorf("creating the %s workspace on the instance: %w\n%s", label, err, out)
	}
	// Read the id back rather than parsing the create reply: one shape to
	// know instead of two, and the listing is authoritative either way.
	out, err = h.Run(workspaceListArgs(machine)...)
	if err != nil {
		return "", fmt.Errorf("listing workspaces after creating %s: %w\n%s", label, err, out)
	}
	workspaces, err = parseWorkspaceList(out)
	if err != nil {
		return "", err
	}
	created, found := findWorkspace(workspaces, label)
	if !found {
		return "", fmt.Errorf("created the %s workspace on the instance but it is not in the listing", label)
	}

	// <-- move the existing "Starting a named session gives it a default ~
	// workspace ..." comment block here verbatim -->
	if def, ok := findWorkspace(workspaces, herdrDefaultWorkspaceLabel); ok && def.Panes <= 1 {
		_, _ = h.Run(workspaceCloseArgs(machine, def.ID)...)
	}
	return created.ID, nil
}

// FocusWorkspace switches the instance's herdr to the session's workspace.
//
// This is the only part of "switch to my session" bivouac can perform.
// Selecting the machine itself is client state with no API, so the caller
// still has to name the sidebar entry for the user to pick.
func FocusWorkspace(h herdrRunner, machine, id string) error {
	if out, err := h.Run(workspaceFocusArgs(machine, id)...); err != nil {
		return fmt.Errorf("focusing workspace %s on the instance: %w\n%s", id, err, out)
	}
	return nil
}
```

(The comment-block placeholder above is an instruction to *move existing text*, not new content: copy the current default-workspace comment from the old `EnsureWorkspace` unchanged.)

- [ ] **Step 4: Rewire `AttachMachine`** in `internal/lifecycle/herdrattach.go` — replace everything from `client, err := reconcile.Connect(...)` to the final `return` with:

```go
	h := localHerdr{ctx: ctx}
	wsID, err := EnsureWorkspace(h, id, RemoteRepoPath(user, session, repoName), session)
	if err != nil {
		return id, label, err
	}
	if err := FocusWorkspace(h, id, wsID); err != nil {
		return id, label, err
	}
	return id, label, nil
```

and pass the same `h` to `EnsureMachine` (replace its `localHerdr{ctx: ctx}` argument). Remove the `reconcile` import. Update the doc comment's "Three steps" paragraph only where it says or implies SSH — the workspace is now reached through the machine that step one saved, which is why the order matters even more: `--machine` needs an enabled profile.

- [ ] **Step 5: Run the lifecycle tests**

Run: `nix develop --command go test ./internal/lifecycle/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: exit=0.

Also: `nix develop --command go vet ./... > /tmp/v.log 2>&1; echo exit=$?` → exit=0 (catches unused imports / dead helpers elsewhere).

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/
git commit -m "Reach the session workspace through --machine" -m "herdr 0.9.1 routes client commands to a saved machine's server, and
AttachMachine has just saved one. Opening a second SSH connection to
run the same commands was slower and one more thing to fail.

The SSH-exec'd path is removed rather than kept beside it: two routes
to the same workspace would drift. The first routed listing doubles as
the capability probe, since herdr refuses status under --machine."
```

---

### Task 3: Lay out `herdrTabs` inside the workspace

**Files:**
- Create: `internal/lifecycle/herdrtabs.go`
- Create: `internal/lifecycle/herdrtabs_test.go`

**Interfaces:**
- Consumes: `machineArgs`, `herdrRunner`, test double `fakeRouted` (Task 2, in `herdrworkspace_test.go`); `config.HerdrTab`, `config.HerdrPane` (Task 1).
- Produces: `func EnsureTabs(h herdrRunner, machine, workspaceID, root string, tabs []config.HerdrTab) error`

- [ ] **Step 1: Write the failing tests** — create `internal/lifecycle/herdrtabs_test.go`:

```go
package lifecycle

import (
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
)

func strp(s string) *string { return &s }

// runTab is the layout most tests use: a server pane, and a logs pane split
// off it in a subdirectory.
var runTab = config.HerdrTab{Label: "run", Panes: []config.HerdrPane{
	{Label: "server", Command: strp("npm run dev")},
	{Label: "logs", Command: strp("tail -f server.log"), Cwd: strp("logs")},
}}

const (
	onlyDefaultTab = `{"result":{"tabs":[{"label":"1","tab_id":"w3:t1","workspace_id":"w3"}],"type":"tab_list"}}`
	onlyRootPane   = `{"result":{"panes":[{"pane_id":"w3:p1","tab_id":"w3:t1","workspace_id":"w3"}],"type":"pane_list"}}`
	tabCreated     = `{"result":{"root_pane":{"pane_id":"w3:p2","tab_id":"w3:t2"},"tab":{"label":"run","tab_id":"w3:t2"},"type":"tab_created"}}`
	paneSplit      = `{"result":{"pane":{"pane_id":"w3:p3","tab_id":"w3:t2"},"type":"pane_info"}}`
)

const checkout = "/home/u/sessions/auth/repo"

func TestEnsureTabs_BuildsATabFromNothing(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{
		"tab list":   {onlyDefaultTab},
		"pane list":  {onlyRootPane},
		"tab create": {tabCreated},
		"pane split": {paneSplit},
	}}

	if err := EnsureTabs(h, "mid", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}

	created := h.ran("tab create")
	if len(created) != 1 {
		t.Fatalf("tab creates = %v, want one", created)
	}
	got := strings.Join(created[0], " ")
	for _, want := range []string{"--workspace w3", "--label run", "--cwd " + checkout, "--no-focus"} {
		if !strings.Contains(got, want) {
			t.Errorf("tab create = %q, want %q", got, want)
		}
	}

	// The second pane splits off the first -- the tab's own root pane --
	// in its subdirectory, without taking focus.
	splits := h.ran("pane split")
	if len(splits) != 1 {
		t.Fatalf("splits = %v, want one", splits)
	}
	split := strings.Join(splits[0], " ")
	if !strings.HasPrefix(split, "w3:p2") || !strings.Contains(split, "--cwd "+checkout+"/logs") || !strings.Contains(split, "--no-focus") {
		t.Errorf("split = %q, want w3:p2 split into %s/logs without focus", split, checkout)
	}

	renames := h.ran("pane rename")
	if len(renames) != 2 || strings.Join(renames[0], " ") != "w3:p2 server" || strings.Join(renames[1], " ") != "w3:p3 logs" {
		t.Errorf("renames = %v, want [w3:p2 server] [w3:p3 logs]", renames)
	}
	runs := h.ran("pane run")
	if len(runs) != 2 || strings.Join(runs[0], " ") != "w3:p2 npm run dev" || strings.Join(runs[1], " ") != "w3:p3 tail -f server.log" {
		t.Errorf("runs = %v, want each command in its own pane", runs)
	}
	for _, m := range h.machines {
		if m != "mid" {
			t.Errorf("routed to %q, want the session's machine", m)
		}
	}
}

// Attaching again must add nothing and, above all, must not run
// `npm run dev` into a pane that is already running it.
func TestEnsureTabs_ReattachChangesNothing(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{
		"tab list": {`{"result":{"tabs":[{"label":"1","tab_id":"w3:t1"},{"label":"run","tab_id":"w3:t2"}]}}`},
		"pane list": {`{"result":{"panes":[{"pane_id":"w3:p1","tab_id":"w3:t1"},` +
			`{"label":"server","pane_id":"w3:p2","tab_id":"w3:t2"},` +
			`{"label":"logs","pane_id":"w3:p3","tab_id":"w3:t2"}]}}`},
	}}

	if err := EnsureTabs(h, "mid", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	for _, verb := range []string{"tab create", "pane split", "pane rename", "pane run"} {
		if got := h.ran(verb); len(got) != 0 {
			t.Errorf("%s ran %v on a workspace that already has the layout", verb, got)
		}
	}
}

// A pane closed since the last attach comes back, split off the one before
// it, and only that pane's command runs.
func TestEnsureTabs_RestoresOnlyAMissingPane(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{
		"tab list": {`{"result":{"tabs":[{"label":"run","tab_id":"w3:t2"}]}}`},
		"pane list": {`{"result":{"panes":[` +
			`{"label":"server","pane_id":"w3:p2","tab_id":"w3:t2"}]}}`},
		"pane split": {paneSplit},
	}}

	if err := EnsureTabs(h, "mid", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if got := h.ran("tab create"); len(got) != 0 {
		t.Errorf("recreated a tab that exists: %v", got)
	}
	splits := h.ran("pane split")
	if len(splits) != 1 || splits[0][0] != "w3:p2" {
		t.Errorf("splits = %v, want one split off the existing server pane", splits)
	}
	runs := h.ran("pane run")
	if len(runs) != 1 || runs[0][0] != "w3:p3" {
		t.Errorf("runs = %v, want only the restored logs pane's command", runs)
	}
}

// A pane with the same label in a different tab is not this tab's pane.
func TestEnsureTabs_MatchesPanesWithinTheirOwnTab(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{
		"tab list": {onlyDefaultTab},
		"pane list": {`{"result":{"panes":[` +
			`{"label":"server","pane_id":"w3:p1","tab_id":"w3:t1"}]}}`},
		"tab create": {tabCreated},
		"pane split": {paneSplit},
	}}

	if err := EnsureTabs(h, "mid", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if runs := h.ran("pane run"); len(runs) != 2 {
		t.Errorf("runs = %v, want both panes of the new tab started", runs)
	}
}

// A tab with no panes is just a shell in the checkout; a pane with no
// command is just a labelled shell.
func TestEnsureTabs_RunsNothingThatWasNotAskedFor(t *testing.T) {
	h := &fakeRouted{replies: map[string][]string{
		"tab list":   {onlyDefaultTab},
		"pane list":  {onlyRootPane},
		"tab create": {tabCreated, tabCreated},
	}}
	tabs := []config.HerdrTab{
		{Label: "shell"},
		{Label: "edit", Panes: []config.HerdrPane{{Label: "code"}}},
	}

	if err := EnsureTabs(h, "mid", "w3", checkout, tabs); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if got := h.ran("tab create"); len(got) != 2 {
		t.Errorf("tab creates = %v, want two", got)
	}
	if got := h.ran("pane run"); len(got) != 0 {
		t.Errorf("ran %v with no command configured", got)
	}
	if got := h.ran("pane rename"); len(got) != 1 {
		t.Errorf("renames = %v, want just the code pane", got)
	}
}

func TestEnsureTabs_DoesNothingWithoutTabs(t *testing.T) {
	h := &fakeRouted{}
	if err := EnsureTabs(h, "mid", "w3", checkout, nil); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("ran %v with no layout configured -- herdrTabs is opt-in", h.calls)
	}
}

func TestEnsureTabs_ReportsAFailedSplit(t *testing.T) {
	h := &fakeRouted{
		replies: map[string][]string{
			"tab list":   {onlyDefaultTab},
			"pane list":  {onlyRootPane},
			"tab create": {tabCreated},
		},
		fail: "pane split",
	}
	err := EnsureTabs(h, "mid", "w3", checkout, []config.HerdrTab{runTab})
	if err == nil || !strings.Contains(err.Error(), "logs") {
		t.Errorf("error = %v, want one naming the logs pane", err)
	}
}

func TestPaneDir_IsRelativeToTheCheckout(t *testing.T) {
	for _, tt := range []struct {
		cwd  *string
		want string
	}{
		{nil, checkout},
		{strp(""), checkout},
		{strp("logs"), checkout + "/logs"},
		{strp("a/../b"), checkout + "/b"},
		{strp("/var/log"), "/var/log"},
	} {
		if got := paneDir(checkout, config.HerdrPane{Cwd: tt.cwd}); got != tt.want {
			t.Errorf("paneDir(%v) = %q, want %q", tt.cwd, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `nix develop --command go test ./internal/lifecycle/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: build failure — `undefined: EnsureTabs`, `undefined: paneDir`.

- [ ] **Step 3: Implement** — create `internal/lifecycle/herdrtabs.go`:

```go
package lifecycle

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/jskswamy/bivouac/internal/config"
)

// herdrTab and herdrPane are entries from `herdr tab list` and `herdr pane
// list`. Like workspace ids, these are scoped to the instance's server and
// read per call, never remembered. A pane nobody has named has no label key
// at all, which decodes to "".
type herdrTab struct {
	ID    string `json:"tab_id"`
	Label string `json:"label"`
}

type herdrPane struct {
	ID    string `json:"pane_id"`
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

func parseTabList(out string) ([]herdrTab, error) {
	var reply struct {
		Result struct {
			Tabs []herdrTab `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return nil, fmt.Errorf("reading the herdr tab list: %w\n%s", err, out)
	}
	return reply.Result.Tabs, nil
}

func parsePaneList(out string) ([]herdrPane, error) {
	var reply struct {
		Result struct {
			Panes []herdrPane `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return nil, fmt.Errorf("reading the herdr pane list: %w\n%s", err, out)
	}
	return reply.Result.Panes, nil
}

// parseTabCreated reads the new tab's id and the root pane it opened with.
// The create reply is used here, unlike for workspaces, because the root
// pane has no label yet and so could not be found again in a listing.
func parseTabCreated(out string) (tabID, rootPane string, err error) {
	var reply struct {
		Result struct {
			Tab      herdrTab  `json:"tab"`
			RootPane herdrPane `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return "", "", fmt.Errorf("reading the herdr tab create reply: %w\n%s", err, out)
	}
	return reply.Result.Tab.ID, reply.Result.RootPane.ID, nil
}

// parsePaneSplit reads the id of the pane a split opened, for the same
// reason: it is unlabelled until renamed.
func parsePaneSplit(out string) (string, error) {
	var reply struct {
		Result struct {
			Pane herdrPane `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return "", fmt.Errorf("reading the herdr pane split reply: %w\n%s", err, out)
	}
	return reply.Result.Pane.ID, nil
}

func tabListArgs(machine, workspace string) []string {
	return machineArgs(machine, "tab", "list", "--workspace", workspace)
}

// tabCreateArgs opens the tab in its first pane's directory, since that
// pane is the tab's own root pane rather than a split. --no-focus for the
// same reason as workspaces: laying out must not move anyone.
func tabCreateArgs(machine, workspace, label, cwd string) []string {
	return machineArgs(machine, "tab", "create", "--workspace", workspace,
		"--label", label, "--cwd", cwd, "--no-focus")
}

func paneListArgs(machine, workspace string) []string {
	return machineArgs(machine, "pane", "list", "--workspace", workspace)
}

// paneSplitArgs leaves the direction to herdr. Declaration order is the
// whole topology the config expresses; a split direction would be one more
// thing to configure for no case seen so far.
func paneSplitArgs(machine, from, cwd string) []string {
	return machineArgs(machine, "pane", "split", from, "--cwd", cwd, "--no-focus")
}

func paneRenameArgs(machine, id, label string) []string {
	return machineArgs(machine, "pane", "rename", id, label)
}

func paneRunArgs(machine, id, command string) []string {
	return machineArgs(machine, "pane", "run", id, command)
}

// paneDir is where a pane starts: its configured cwd, relative to the
// session's checkout, or the checkout itself.
//
// path, not filepath: this names a directory on the instance, which is
// Linux whatever this machine is.
func paneDir(root string, p config.HerdrPane) string {
	if p.Cwd == nil || *p.Cwd == "" {
		return root
	}
	if path.IsAbs(*p.Cwd) {
		return *p.Cwd
	}
	return path.Join(root, *p.Cwd)
}

// EnsureTabs lays out the configured tabs and panes in the session's
// workspace, adding only what is not already there.
//
// Everything is matched by label, the same way the workspace is: ids are the
// instance server's, and the label is the only part bivouac chose. That
// also decides when a command runs -- only for a pane this call created. A
// second attach finds every label present and runs nothing, which is what
// keeps `npm run dev` from being typed into a pane already running it.
//
// herdr's own default tab ("1") is left alone; configured tabs follow it.
func EnsureTabs(h herdrRunner, machine, workspaceID, root string, tabs []config.HerdrTab) error {
	if len(tabs) == 0 {
		return nil
	}
	out, err := h.Run(tabListArgs(machine, workspaceID)...)
	if err != nil {
		return fmt.Errorf("listing tabs: %w\n%s", err, out)
	}
	existing, err := parseTabList(out)
	if err != nil {
		return err
	}
	out, err = h.Run(paneListArgs(machine, workspaceID)...)
	if err != nil {
		return fmt.Errorf("listing panes: %w\n%s", err, out)
	}
	panes, err := parsePaneList(out)
	if err != nil {
		return err
	}
	for _, t := range tabs {
		if err := ensureTab(h, machine, workspaceID, root, t, existing, panes); err != nil {
			return err
		}
	}
	return nil
}

// ensureTab brings one tab up to its configuration.
//
// Panes chain in declaration order: the first is the tab's root pane and
// each later one splits off the pane before it. When a pane is already
// there it becomes the next split's anchor, so restoring one closed pane
// puts it back beside its neighbour rather than at the end.
func ensureTab(h herdrRunner, machine, workspaceID, root string, t config.HerdrTab, existing []herdrTab, panes []herdrPane) error {
	var tabID, fresh string
	if found, ok := findTab(existing, t.Label); ok {
		tabID = found.ID
	} else {
		dir := root
		if len(t.Panes) > 0 {
			dir = paneDir(root, t.Panes[0])
		}
		out, err := h.Run(tabCreateArgs(machine, workspaceID, t.Label, dir)...)
		if err != nil {
			return fmt.Errorf("creating the %s tab: %w\n%s", t.Label, err, out)
		}
		if tabID, fresh, err = parseTabCreated(out); err != nil {
			return err
		}
	}

	inTab := panesInTab(panes, tabID)
	prev := fresh
	if len(inTab) > 0 {
		prev = inTab[0].ID
	}
	for i, p := range t.Panes {
		if found, ok := findPane(inTab, p.Label); ok {
			prev = found.ID
			continue
		}
		id := fresh
		if i > 0 || fresh == "" {
			out, err := h.Run(paneSplitArgs(machine, prev, paneDir(root, p))...)
			if err != nil {
				return fmt.Errorf("opening the %s pane in the %s tab: %w\n%s", p.Label, t.Label, err, out)
			}
			if id, err = parsePaneSplit(out); err != nil {
				return err
			}
		}
		if out, err := h.Run(paneRenameArgs(machine, id, p.Label)...); err != nil {
			return fmt.Errorf("labelling the %s pane in the %s tab: %w\n%s", p.Label, t.Label, err, out)
		}
		if p.Command != nil && *p.Command != "" {
			if out, err := h.Run(paneRunArgs(machine, id, *p.Command)...); err != nil {
				return fmt.Errorf("starting %q in the %s pane: %w\n%s", *p.Command, p.Label, err, out)
			}
		}
		prev = id
	}
	return nil
}

func findTab(tabs []herdrTab, label string) (herdrTab, bool) {
	for _, t := range tabs {
		if t.Label == label {
			return t, true
		}
	}
	return herdrTab{}, false
}

func panesInTab(panes []herdrPane, tabID string) []herdrPane {
	var out []herdrPane
	for _, p := range panes {
		if p.TabID == tabID {
			out = append(out, p)
		}
	}
	return out
}

func findPane(panes []herdrPane, label string) (herdrPane, bool) {
	for _, p := range panes {
		if p.Label == label {
			return p, true
		}
	}
	return herdrPane{}, false
}
```

Note on `id := fresh` / `if i > 0 || fresh == ""`: the root pane of a tab created in this call is used only for the first declared pane; every other missing pane is a split. Confirm the tests cover both branches (they do: BuildsATabFromNothing and RestoresOnlyAMissingPane).

- [ ] **Step 4: Run the lifecycle tests**

Run: `nix develop --command go test ./internal/lifecycle/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: exit=0.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/herdrtabs.go internal/lifecycle/herdrtabs_test.go
git commit -m "Lay out configured herdr tabs and panes" -m "EnsureTabs builds the tabs herdrTabs declares inside a session's
workspace, over the same --machine route as the workspace itself.

Matched by label so a second attach adds nothing, and a pane's command
runs only in the call that created it: re-attaching must not type
npm run dev into a pane that is already running it."
```

---

### Task 4: Wire tabs into `bivouac herdr`

**Files:**
- Modify: `internal/lifecycle/herdrattach.go` (`AttachMachine` signature + tabs step)
- Modify: `cmd/run_attach.go` (`runHerdr`, new `herdrTabsFor`)
- Test: `cmd/run_attach_test.go` (create if absent; otherwise append)

**Interfaces:**
- Consumes: `EnsureWorkspace`, `FocusWorkspace` (Task 2), `EnsureTabs` (Task 3), `config.Resolve`, `config.HerdrTab` (Task 1).
- Produces: `func AttachMachine(ctx context.Context, instance, ip, user, session, repoName string, tabs []config.HerdrTab, owned OwnedMachines) (string, string, error)`; `func herdrTabsFor(ctx context.Context, localRepo string) []config.HerdrTab` in package `cmd`.

- [ ] **Step 1: Write the failing tests** — in `cmd/run_attach_test.go`. First read an existing `cmd/*_test.go` that exercises `config.Resolve` (or `internal/config/main_test.go`) to see how tests keep the developer's real `~/.config/bivouac/base.pkl` out (e.g. `t.Setenv("XDG_CONFIG_HOME", t.TempDir())`) and do the same.

```go
func TestHerdrTabsFor_NoConfigMeansNoLayout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := herdrTabsFor(context.Background(), t.TempDir()); len(got) != 0 {
		t.Errorf("herdrTabsFor() = %+v, want none for a repository without bivouac.pkl", got)
	}
	if got := herdrTabsFor(context.Background(), ""); len(got) != 0 {
		t.Errorf("herdrTabsFor(\"\") = %+v, want none", got)
	}
}

func TestHerdrTabsFor_ReadsTheRepositorysTabs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := t.TempDir()
	content := "region = \"nyc3\"\nsize = \"s\"\ntemplate = \"python\"\n" +
		"herdrTabs {\n  new HerdrTab {\n    label = \"run\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "bivouac.pkl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := herdrTabsFor(context.Background(), repo)
	if len(got) != 1 || got[0].Label != "run" {
		t.Errorf("herdrTabsFor() = %+v, want the run tab", got)
	}
}

// A config that will not resolve costs the layout, not the attach.
func TestHerdrTabsFor_BrokenConfigDegradesToNoLayout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "bivouac.pkl"), []byte("herdrTabs = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := herdrTabsFor(context.Background(), repo); len(got) != 0 {
		t.Errorf("herdrTabsFor() = %+v, want none", got)
	}
}
```

If `provider.ReportWarning` needs a reporter in the context to avoid a panic or noisy output, follow what other `cmd` tests do.

- [ ] **Step 2: Run to verify failure**

Run: `nix develop --command go test ./cmd/ > /tmp/t.log 2>&1; echo exit=$?`
Expected: build failure — `undefined: herdrTabsFor`.

- [ ] **Step 3: Implement `herdrTabsFor`** in `cmd/run_attach.go` (add imports `context`, `os`, `path/filepath`, `internal/config`, `internal/provider` as needed):

```go
// herdrTabsFor reads the tab layout the session's repository asks for.
//
// Best-effort, like beadsModeFor: `bivouac herdr` has never needed a
// config to resolve, and the layout must not become the reason it fails.
// A repository with no bivouac.pkl has nothing to lay out -- base.pkl's
// tabs included, since Resolve is anchored on the project file.
func herdrTabsFor(ctx context.Context, localRepo string) []config.HerdrTab {
	if localRepo == "" {
		return nil
	}
	path := filepath.Join(localRepo, "bivouac.pkl")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cfg, err := config.Resolve(ctx, path)
	if err != nil {
		provider.ReportWarning(ctx, "herdr: could not resolve "+path+" ("+err.Error()+"); attaching without its tabs")
		return nil
	}
	return cfg.HerdrTabs
}
```

- [ ] **Step 4: Thread tabs through `AttachMachine`** — in `internal/lifecycle/herdrattach.go`, change the signature to

```go
func AttachMachine(ctx context.Context, instance, ip, user, session, repoName string, tabs []config.HerdrTab, owned OwnedMachines) (string, string, error) {
```

and make the body after `EnsureMachine`:

```go
	h := localHerdr{ctx: ctx}
	root := RemoteRepoPath(user, session, repoName)
	wsID, err := EnsureWorkspace(h, id, root, session)
	if err != nil {
		return id, label, err
	}
	// Layout is best-effort: the machine is saved and the workspace exists,
	// which is what attaching needs. A tab that would not open is worth
	// saying, not worth refusing the attach over.
	if err := EnsureTabs(h, id, wsID, root, tabs); err != nil {
		provider.ReportWarning(ctx, "herdr: laying out tabs: "+err.Error())
	}
	if err := FocusWorkspace(h, id, wsID); err != nil {
		return id, label, err
	}
	return id, label, nil
```

(Import `internal/config`; `provider` is already imported.) Update the doc comment: the steps are now machine → workspace → tabs → focus; focus stays last because it is the only step that moves anyone.

- [ ] **Step 5: Update `runHerdr`** in `cmd/run_attach.go`:
  - capture `localRepo := ""` next to `session`/`repoName`, set `localRepo = sess.LocalRepo` in the resolve branch;
  - call `lifecycle.AttachMachine(cmd.Context(), record.Name, record.IP, record.User, session, repoName, herdrTabsFor(cmd.Context(), localRepo), ownedMachines(store))`;
  - record the id **before** returning an attach error, since `AttachMachine` now returns a saved profile's id alongside workspace errors (e.g. the provision probe) and an unrecorded profile is one teardown can never remove:

```go
		id, label, err := lifecycle.AttachMachine(cmd.Context(), record.Name, record.IP,
			record.User, session, repoName, herdrTabsFor(cmd.Context(), localRepo), ownedMachines(store))
		// Recorded before the error is looked at: AttachMachine hands back a
		// saved profile's id even when a later step failed, and teardown
		// removes this exact profile -- one bivouac created but did not
		// record is one nothing will ever clean up.
		if rerr := recordHerdrMachine(store, record.Name, session, id); rerr != nil {
			return errors.Join(err, rerr)
		}
		if err != nil {
			return err
		}
```

  Also update the now-stale comment above `session := ""` that says herdr "has no way to attach a starting directory ... (that's cloudlab-7y1 ...)": outside herdr that is still true; inside herdr the workspace is rooted in the checkout by `AttachMachine`. One or two sentences.

- [ ] **Step 6: Run the full suite and vet**

Run: `nix develop --command go test ./... > /tmp/t.log 2>&1; echo exit=$?` then `nix develop --command go vet ./... > /tmp/v.log 2>&1; echo exit=$?`
Expected: both exit=0. Read `/tmp/t.log` for any `FAIL` lines if not.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/lifecycle/herdrattach.go
git commit -m "Apply herdrTabs when attaching inside herdr" -m "bivouac herdr reads the session repository's herdrTabs and lays them
out after ensuring the workspace, before focusing it. Best-effort on
both ends: a config that will not resolve or a tab that will not open
costs the layout, never the attach.

The machine id is now recorded before an attach error is returned.
AttachMachine hands back a saved profile's id alongside later failures,
and a profile bivouac saved but did not record would never be cleaned
up at teardown."
```

---

### Task 5: Document `herdrTabs` and reconcile the spec

**Files:**
- Modify: `docs/config.md`
- Modify: `docs/superpowers/specs/2026-09-18-herdr-machine-workspace-design.md`

- [ ] **Step 1: `docs/config.md`** — add a row to the field table after `flakes` (line ~37), matching its column format:

```markdown
| `herdrTabs` | `Listing<HerdrTab>` (`{label, panes}`; each pane `{label, command?, cwd?}`) | No | empty | Tabs and panes `bivouac herdr` lays out in the session's workspace when run inside herdr. Merges additively like `packages`, base's tabs first. See [herdr tabs](#herdr-tabs). |
```

Add `herdrTabs` to the list of additive fields near line 283 (`- **Lists** (...)`). Then add a `## herdr tabs` section (place it near the other feature sections; follow their heading style) containing: the sample from the spec's section 2 written with one property per line (no `;`), and four short paragraphs — panes chain in declaration order (first is the tab's own pane, each later one splits off the previous); matched by label, so re-attaching adds only missing tabs/panes; a pane's `command` runs only when that pane is created; `cwd` is relative to the session checkout on the instance, omitted means the checkout; herdr's own default tab is left alone and configured tabs follow it; layout needs `bivouac herdr` to be run from inside herdr and the repository to have a `bivouac.pkl`.

- [ ] **Step 2: Spec** — in the 09-18 spec, change `Status:` to `implemented` and append a section:

```markdown
## Implementation notes

Where the implementation departs from the design above, verified against
herdr 0.9.1 while planning:

- `tab create` accepts `--label`, so tabs are labelled at creation. Only
  panes need a `pane rename` after `pane split`.
- `herdr --machine <x> status` is refused ("not an API-backed machine
  command"), so the capability gate cannot be a status call. The first
  routed call, `workspace list`, is the probe; forwarding never installs,
  restarts or falls back to a local server, so asking with a real command
  is safe. Its failure names `bivouac provision`.
- The `--machine` selector is the profile id, not the label: herdr accepts
  a label only while it is unique, and the user may rename it.
- The SSH path already parsed JSON; `parseWorkspaceList` is reused
  unchanged. With the SSH path gone, the functions keep the names
  `EnsureWorkspace`/`FocusWorkspace` rather than gaining a `ViaMachine`
  suffix.
- Tab and pane failures warn rather than fail the attach.
- herdr's default tab ("1") is left in place; configured tabs follow it.
- `herdrTabs` is read from the session's local `bivouac.pkl`; a repository
  without one gets no layout, including base.pkl's tabs.
```

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "Document herdrTabs and the --machine departures" -m "The spec is the implementation contract, so the places the code
departs from it -- tab labels at creation, the probe replacing a status
gate herdr refuses under --machine, selecting by profile id -- are
written down rather than left for the next reader to rediscover."
```

---

## Final verification (after all tasks)

- [ ] `nix develop --command go test ./... > /tmp/t.log 2>&1; echo exit=$?` → exit=0
- [ ] `nix develop --command pre-commit run --all-files > /tmp/pc.log 2>&1; echo exit=$?` → exit=0 (gofmt, golangci-lint, etc.)
- [ ] `git status --short` → clean; no scratch files in the tree.
- [ ] `grep -rn "reconcile.Connect" internal/lifecycle/herdrattach.go` → no match.
