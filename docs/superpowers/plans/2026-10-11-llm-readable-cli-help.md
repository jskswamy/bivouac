# LLM-readable CLI Help Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make bivouac easier for coding agents to discover and consume by adding MCP-shaped command metadata, `bivouac manifest`, richer Herdr help, and explicit JSON output for core read-only commands.

**Architecture:** Keep Cobra as the source of command structure and add typed metadata beside command construction. Generate a deterministic MCP-shaped manifest from the real Cobra tree without executing command actions. Add `--json` only to selected commands and route those commands through typed render helpers so human output remains unchanged.

**Tech Stack:** Go, Cobra, standard-library `encoding/json`, existing `state`, `lifecycle`, and command test helpers.

**Spec:** `docs/superpowers/specs/2026-10-11-llm-readable-cli-help-design.md`

## Global Constraints

- Human output remains the default; no command silently switches to JSON because an agent environment variable is present.
- `bivouac manifest` is read-only and offline: it must not inspect state, call providers, call git, connect to Herdr, or execute command `RunE` functions.
- The manifest is MCP-shaped tools metadata, not a full MCP server.
- JSON output is explicit via `--json` and must be valid, deterministic, and not table text wrapped in strings.
- Existing command behavior and argument parsing must not change except for explicit `--json` output support.
- Write tests before implementation; run Go tests with redirection, not pipes.

## Review Focus

- Running `bivouac manifest` outside a git repo and with no state file should still succeed; Task 2 tests this.
- Manifest generation must not invoke command actions or identity/session resolution; Task 1 tests this with a command whose `RunE` fails if called.
- Agent environment markers must not change default command output; Task 5 tests human output remains human when `CLAUDECODE=1` is set.
- `--json` must produce clean JSON without ANSI/table formatting; Tasks 3 and 4 test unmarshalling and absence of table headers.
- Unsupported commands must not advertise `jsonOutput: true`; Task 2 tests Herdr metadata remains explicit about whether JSON output is supported.

---

## File Structure

- Create `cmd/metadata.go`: typed command metadata, registry helpers, and JSON Schema helper types used by the manifest.
- Create `cmd/manifest.go`: manifest data structures, command-tree walker, input schema generation, and `newManifestCmd`.
- Create `cmd/json_output.go`: shared `--json` helpers and JSON encoder helper.
- Modify `cmd/root.go`: register `bivouac manifest`.
- Modify `cmd/lookup.go`: extend `lookupCommandSpec` with `example` and `metadata`; apply Herdr/session/status metadata; attach metadata when commands are built.
- Modify `cmd/list.go`: add `--json` and typed instance-list JSON output.
- Modify `cmd/run_session.go`: add `--json` to `session list` and typed session-list JSON output.
- Modify `cmd/run_instance.go` and/or `cmd/status_print.go`: add `--json` to `status` and typed status JSON output.
- Test in new `cmd/manifest_test.go`, new `cmd/json_output_test.go`, and existing command tests where the fixture helpers already live.

---

### Task 1: Command metadata model and manifest generator

**Files:**
- Create: `cmd/metadata.go`
- Create: `cmd/manifest.go`
- Test: `cmd/manifest_test.go`

**Interfaces:**
- Produces: `type commandMetadata struct`, `type argumentMetadata struct`, `func setCommandMetadata(cmd *cobra.Command, meta commandMetadata)`, `func metadataFor(cmd *cobra.Command) commandMetadata`, `func buildManifest(root *cobra.Command) cliManifest`
- Consumes: Cobra command tree only; no state/provider/git/lifecycle calls.

- [ ] **Step 1: Write failing tests for metadata and offline manifest walking**

Add `cmd/manifest_test.go` with:

```go
func TestBuildManifest_DoesNotRunCommands(t *testing.T) {
    root := &cobra.Command{Use: "bivouac"}
    child := &cobra.Command{
        Use: "explode [name]",
        Short: "must not run",
        RunE: func(cmd *cobra.Command, args []string) error {
            t.Fatal("RunE was invoked during manifest generation")
            return nil
        },
    }
    child.Flags().String("mode", "", "mode to use")
    setCommandMetadata(child, commandMetadata{
        Title: "Explode test command",
        Interactive: true,
        Network: true,
        Arguments: []argumentMetadata{{
            Name: "name", Required: false, Description: "optional name",
            Schema: map[string]any{"type": "string"},
        }},
    })
    root.AddCommand(child)

    got := buildManifest(root)
    if got.SchemaVersion != "1" { t.Fatalf("SchemaVersion = %q", got.SchemaVersion) }
    tool := findTool(t, got, "bivouac_explode")
    if tool.Title != "Explode test command" { t.Fatalf("Title = %q", tool.Title) }
    if tool.Metadata.Interactive != true || tool.Metadata.Network != true { t.Fatalf("metadata = %+v", tool.Metadata) }
    if _, ok := tool.InputSchema.Properties["mode"]; !ok { t.Fatalf("mode flag missing from input schema") }
    if _, ok := tool.InputSchema.Properties["name"]; !ok { t.Fatalf("name argument missing from input schema") }
}
```

Also add `TestBuildManifest_IsDeterministic`, calling `buildManifest(root)` twice, `json.MarshalIndent` both values, and asserting the strings match.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd -run 'TestBuildManifest' > /tmp/bivouac-test.log 2>&1`

Expected: FAIL because manifest types/functions do not exist.

- [ ] **Step 3: Implement metadata and manifest types**

In `cmd/metadata.go`, implement:

```go
type commandMetadata struct {
    Title            string
    ToolName         string
    Interactive      bool
    Network          bool
    Destructive      bool
    RequiresInstance bool
    RequiresRepo     bool
    MayPrompt        bool
    SupportsJSON     bool
    SideEffects      []string
    Related          []string
    Arguments        []argumentMetadata
    OutputSchema     *jsonSchema
}

type argumentMetadata struct {
    Name        string
    Required    bool
    Description string
    Schema      map[string]any
}

type jsonSchema struct {
    Type                 string                `json:"type,omitempty"`
    Properties           map[string]any        `json:"properties,omitempty"`
    Required             []string              `json:"required,omitempty"`
    AdditionalProperties bool                  `json:"additionalProperties"`
}
```

Use a package-level `map[*cobra.Command]commandMetadata` for metadata. Keep helper functions package-private.

- [ ] **Step 4: Implement `buildManifest(root *cobra.Command) cliManifest`**

In `cmd/manifest.go`, define serializable structs with JSON field names from the spec: `schemaVersion`, `name`, `summary`, `tools`, `inputSchema`, `outputSchema`, `examples`, and `metadata`. Walk `root.Commands()` recursively in sorted command-path order. Build tool names as `bivouac_` plus command path joined by `_` unless `ToolName` is set. Include local flags and inherited persistent flags as string/boolean/integer schema properties based on pflag value type.

- [ ] **Step 5: Run tests to verify Task 1 passes**

Run: `go test ./cmd -run 'TestBuildManifest' > /tmp/bivouac-test.log 2>&1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/metadata.go cmd/manifest.go cmd/manifest_test.go
git commit -m "Add CLI manifest metadata model"
```

---

### Task 2: Manifest command and Herdr/help metadata

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/lookup.go`
- Modify: `cmd/manifest.go`
- Test: `cmd/manifest_test.go`

**Interfaces:**
- Consumes: `buildManifest(root *cobra.Command) cliManifest`, `setCommandMetadata` from Task 1.
- Produces: `newManifestCmd(rootForManifest func() *cobra.Command) *cobra.Command` or equivalent registration that avoids recursive manifest output.

- [ ] **Step 1: Write failing manifest command tests**

Add tests:

```go
func TestManifestCommand_PrintsMCPToolsManifest(t *testing.T) {
    cmd := newRootCmd()
    buf := new(bytes.Buffer)
    cmd.SetOut(buf)
    cmd.SetErr(buf)
    cmd.SetArgs([]string{"manifest", "--format", "mcp-tools"})

    if err := cmd.Execute(); err != nil { t.Fatal(err) }
    var got cliManifest
    if err := json.Unmarshal(buf.Bytes(), &got); err != nil { t.Fatalf("invalid json: %v\n%s", err, buf.String()) }
    if got.SchemaVersion != "1" || got.Name != "bivouac" { t.Fatalf("manifest header = %+v", got) }
    herdr := findTool(t, got, "bivouac_herdr")
    if herdr.Title != "Attach Herdr to a bivouac instance/session" { t.Fatalf("herdr title = %q", herdr.Title) }
    if herdr.Metadata.Interactive != true || herdr.Metadata.Network != true || herdr.Metadata.Destructive != false { t.Fatalf("herdr metadata = %+v", herdr.Metadata) }
    if herdr.Metadata.JsonOutput { t.Fatalf("herdr must not advertise jsonOutput until --json is implemented for it") }
    if _, ok := herdr.InputSchema.Properties["name"]; !ok { t.Fatalf("herdr input schema missing positional name") }
    if _, ok := herdr.InputSchema.Properties["repo"]; !ok { t.Fatalf("herdr input schema missing persistent --repo") }
}

func TestManifestCommand_IncludesNestedSessionStart(t *testing.T) {
    got := manifestFromCommand(t, []string{"manifest"})
    start := findTool(t, got, "bivouac_session_start")
    if _, ok := start.InputSchema.Properties["name"]; !ok { t.Fatalf("session start missing name argument") }
    if _, ok := start.InputSchema.Properties["task"]; !ok { t.Fatalf("session start missing --task") }
    if _, ok := start.InputSchema.Properties["issue"]; !ok { t.Fatalf("session start missing --issue") }
}
```

Add `TestHerdrHelpMentionsLLMRelevantBehavior`, executing `newRootCmd()` with `herdr --help`, and assert the output contains `instance name, not a session name`, `outside Herdr`, `inside Herdr`, `saved machine`, and `workspace`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd -run 'TestManifestCommand|TestHerdrHelp' > /tmp/bivouac-test.log 2>&1`

Expected: FAIL because `manifest` command and Herdr long help are not wired.

- [ ] **Step 3: Register `bivouac manifest`**

In `cmd/root.go`, add `root.AddCommand(newManifestCmd())`. Implement `newManifestCmd` in `cmd/manifest.go` with `Use: "manifest"`, `--format` defaulting to `mcp-tools`, and an error for any other format. Its `RunE` should build a fresh root command, remove or hide the manifest command if needed to avoid recursive self-description, call `buildManifest`, and JSON-encode to `cmd.OutOrStdout()` with deterministic indentation.

- [ ] **Step 4: Extend lookup specs with help examples and metadata**

In `cmd/lookup.go`, add fields to `lookupCommandSpec`:

```go
example string
metadata commandMetadata
```

Set `Example: spec.example` and call `setCommandMetadata(c, spec.metadata)` after command construction. For Herdr, set:

- `Title: "Attach Herdr to a bivouac instance/session"`
- `Interactive: true`, `Network: true`, `Destructive: false`, `RequiresInstance: true`, `MayPrompt: true`
- `Arguments`: optional `name` string described as `Optional bivouac instance name, not session name`
- `SideEffects`: `may register a Herdr saved machine`, `may create or focus a Herdr workspace`
- `Related`: `session list`, `session start`, `ssh`, `tmux`

Update Herdr `long` and `example` with the exact concepts asserted in the tests. Add metadata for `session start`, `session list`, `status`, and `list` sufficient for the manifest tests and later JSON tasks.

- [ ] **Step 5: Run tests to verify Task 2 passes**

Run: `go test ./cmd -run 'TestManifestCommand|TestHerdrHelp|TestBuildManifest' > /tmp/bivouac-test.log 2>&1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/root.go cmd/lookup.go cmd/manifest.go cmd/manifest_test.go
git commit -m "Expose MCP-shaped CLI manifest"
```

---

### Task 3: JSON output for `bivouac list`

**Files:**
- Create: `cmd/json_output.go`
- Modify: `cmd/list.go`
- Test: `cmd/json_output_test.go`

**Interfaces:**
- Consumes: state records from `state.Open().List()` and existing `liveVMs` when `--cost` is set.
- Produces: `func writeJSON(cmd *cobra.Command, v any) error`, `func jsonFlag(cmd *cobra.Command) bool`, and JSON structs for instance list output.

- [ ] **Step 1: Write failing tests for `list --json`**

Add `cmd/json_output_test.go` with a fixture that sets `XDG_STATE_HOME`, writes two `state.Record`s, runs `newRootCmd()` with `list --json`, unmarshals the output, and asserts:

```go
var got struct {
    Instances []struct {
        Name string `json:"name"`
        Provider string `json:"provider"`
        IP string `json:"ip"`
    } `json:"instances"`
}
if len(got.Instances) != 2 { t.Fatalf("instances = %d", len(got.Instances)) }
if strings.Contains(buf.String(), "NAME") { t.Fatalf("json output contains table header: %s", buf.String()) }
```

Also add `TestListJSON_EmptyState` asserting `{"instances":[]}` unmarshals with length zero rather than printing `no instances`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd -run 'TestListJSON' > /tmp/bivouac-test.log 2>&1`

Expected: FAIL because `--json` is unknown.

- [ ] **Step 3: Implement shared JSON helpers**

In `cmd/json_output.go`, implement `writeJSON(cmd, v)` using `json.NewEncoder(cmd.OutOrStdout())`, `SetIndent("", "  ")`, and a trailing newline. Implement `jsonFlag(cmd)` to read `--json` from the command's flags.

- [ ] **Step 4: Add `--json` to `newListCmd`**

In `cmd/list.go`, add `c.Flags().Bool("json", false, "print machine-readable JSON")`. In `RunE`, after records and optional `vms` are loaded, branch to `printInstancesJSON(cmd, records, vms, time.Now())` when `--json` is true. Empty records should return JSON with an empty `instances` array. Include `status` and `cost` fields only when `--cost` is set, mirroring human behavior.

- [ ] **Step 5: Mark list metadata as JSON-capable**

Where `newListCmd` is constructed, attach `commandMetadata{Title: "List bivouac instances", SupportsJSON: true, ...}` so the manifest advertises `jsonOutput: true` for `bivouac_list`.

- [ ] **Step 6: Run tests to verify Task 3 passes**

Run: `go test ./cmd -run 'TestListJSON|TestManifestCommand' > /tmp/bivouac-test.log 2>&1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/json_output.go cmd/list.go cmd/json_output_test.go
git commit -m "Add JSON output for instance list"
```

---

### Task 4: JSON output for session discovery and status

**Files:**
- Modify: `cmd/lookup.go`
- Modify: `cmd/run_session.go`
- Modify: `cmd/run_instance.go`
- Modify: `cmd/status_print.go`
- Test: `cmd/json_output_test.go`

**Interfaces:**
- Consumes: `writeJSON` and `jsonFlag` from Task 3.
- Produces: `session list --json` output object with `sessions`, and `status --json` output object with `instance`, `sessions`, and optional `serving` data.

- [ ] **Step 1: Write failing tests for `session list --json`**

Add a test that writes a state record with one session and runs `session list --json`. Unmarshal:

```go
var got struct {
    Sessions []struct {
        Name string `json:"name"`
        Instance string `json:"instance"`
        Branch string `json:"branch"`
        WorktreeExists bool `json:"worktreeExists"`
        WorktreeDirty bool `json:"worktreeDirty"`
    } `json:"sessions"`
}
```

Assert length one, `Name` and `Instance` match, and output does not contain `NAME`.

- [ ] **Step 2: Write failing tests for `status --json` without provider token**

Add a test that writes one state record and runs `status myinstance --json`. Unmarshal:

```go
var got struct {
    Instance struct {
        Name string `json:"name"`
        IP string `json:"ip"`
        LiveStatus string `json:"liveStatus"`
        LiveError string `json:"liveError,omitempty"`
    } `json:"instance"`
    Sessions []struct { Name string `json:"name"` } `json:"sessions"`
}
```

Assert `Instance.Name == "myinstance"`, `LiveStatus == "unknown"`, and no table labels such as `Provider` or `SESSIONS` appear in the output.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cmd -run 'TestSessionListJSON|TestStatusJSON' > /tmp/bivouac-test.log 2>&1`

Expected: FAIL because these commands do not accept `--json`.

- [ ] **Step 4: Add `--json` to metadata-capable lookup specs**

In `cmd/lookup.go`, add `--json` flags for specs whose metadata has `SupportsJSON: true`, or add the flag directly to `session list` and `status` specs. Set `SupportsJSON: true` for `session list` and `status`.

- [ ] **Step 5: Implement `runSessionList` JSON branch**

In `cmd/run_session.go`, after `infos` is built and before human empty/table output, branch when `jsonFlag(cmd)` is true and write `{"sessions": [...]}`. Empty sessions should encode as an empty array, not `no sessions`.

- [ ] **Step 6: Implement `runStatus` JSON branch**

In `cmd/run_instance.go`, build the same `lifecycle.InstanceStatus` as the human path. If `--json` is set, write a structured object instead of calling `printStatus`, `printSessions`, or `printServing`. Preserve the same degraded live-status behavior when provider resolution fails. Do not perform the serving SSH lookup in the JSON branch unless the output schema includes `serving`; for the first pass, omit serving from JSON to keep status JSON local/provider-only and deterministic under test.

- [ ] **Step 7: Run tests to verify Task 4 passes**

Run: `go test ./cmd -run 'TestSessionListJSON|TestStatusJSON|TestManifestCommand' > /tmp/bivouac-test.log 2>&1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/lookup.go cmd/run_session.go cmd/run_instance.go cmd/status_print.go cmd/json_output_test.go
git commit -m "Add JSON output for session status"
```

---

### Task 5: Agent detection hints without output auto-switching

**Files:**
- Create: `cmd/agent_context.go`
- Modify: `cmd/root.go` or command error/help path only if needed
- Test: `cmd/json_output_test.go` or new `cmd/agent_context_test.go`

**Interfaces:**
- Produces: `func runningUnderAgent() bool` and `func agentJSONHint() string`.
- Consumes: environment variables `BIVOUAC_AGENT`, `CLAUDECODE`, `AI_AGENT`, `PI_CODING_AGENT`.

- [ ] **Step 1: Write failing tests for best-effort detection and no auto-switch**

Add `cmd/agent_context_test.go`:

```go
func TestRunningUnderAgentRecognizesKnownMarkers(t *testing.T) {
    t.Setenv("CLAUDECODE", "1")
    if !runningUnderAgent() { t.Fatal("CLAUDECODE=1 not detected") }
}

func TestAgentMarkerDoesNotSwitchListToJSON(t *testing.T) {
    t.Setenv("CLAUDECODE", "1")
    writeOneRecordFixture(t)
    out := executeRoot(t, "list")
    if strings.HasPrefix(strings.TrimSpace(out), "{") { t.Fatalf("list defaulted to JSON under agent: %s", out) }
    if !strings.Contains(out, "NAME") { t.Fatalf("list did not print human table: %s", out) }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd -run 'TestRunningUnderAgent|TestAgentMarker' > /tmp/bivouac-test.log 2>&1`

Expected: FAIL because `runningUnderAgent` does not exist.

- [ ] **Step 3: Implement detection helper without wiring output changes**

In `cmd/agent_context.go`, implement `runningUnderAgent()` returning true for `BIVOUAC_AGENT` non-empty, `CLAUDECODE=1`, `PI_CODING_AGENT=true`, or `AI_AGENT` non-empty. Add `agentJSONHint()` returning `Tip: use 'bivouac manifest' or '--json' for machine-readable output.` Leave command default output unchanged.

- [ ] **Step 4: Optionally add hints to help/error text only**

If adding a hint is straightforward without disrupting existing tests, append the hint to root help or selected errors only when `runningUnderAgent()` is true. Do not add hints to JSON output.

- [ ] **Step 5: Run tests to verify Task 5 passes**

Run: `go test ./cmd -run 'TestRunningUnderAgent|TestAgentMarker|TestListJSON' > /tmp/bivouac-test.log 2>&1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent_context.go cmd/agent_context_test.go cmd/root.go
git commit -m "Detect agent context without changing output"
```

---

### Task 6: Full verification and documentation polish

**Files:**
- Modify if needed: `README.md` or command help text only for small usage notes
- Test: existing suite

**Interfaces:**
- Consumes: all prior tasks.
- Produces: verified branch ready for review.

- [ ] **Step 1: Run the full Go test suite**

Run: `go test ./... > /tmp/bivouac-test.log 2>&1`

Expected: PASS. If it fails, inspect `/tmp/bivouac-test.log`, fix the failing task's code/tests, and rerun with redirection.

- [ ] **Step 2: Manually inspect key CLI outputs**

Run:

```bash
go run . herdr --help > /tmp/bivouac-herdr-help.txt 2>&1
go run . manifest > /tmp/bivouac-manifest.json 2>&1
go run . list --json > /tmp/bivouac-list-json.txt 2>&1 || true
```

Expected: Herdr help contains the new explanatory text; manifest is valid JSON; `list --json` either prints valid JSON for current state or a clear state error, not a table.

- [ ] **Step 3: Run formatting and targeted lint checks available locally**

Run:

```bash
gofmt -w cmd/metadata.go cmd/manifest.go cmd/json_output.go cmd/agent_context.go cmd/*.go
```

Then rerun: `go test ./... > /tmp/bivouac-test.log 2>&1`

Expected: PASS.

- [ ] **Step 4: Commit final polish if needed**

If Step 2 or 3 changed files:

```bash
git add README.md cmd
git commit -m "Polish LLM-friendly CLI output"
```

- [ ] **Step 5: Run required finishing checks before handoff**

Run the project-required post-implementation review steps from `AGENTS.md`: `refactor:scan` against changed code and `commit-tools:review-commits` to fold TDD churn into logical commits before final handoff.
