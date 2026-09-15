# Agent Context In Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver cloudlab's session facts plus a user-supplied workflow to
every configured coding agent on an instance, and tell that agent what the
session was started for.

**Architecture:** A new `instructions` field names Markdown files; a new
`internal/agentcontext` package renders cloudlab's own block, appends those
files verbatim, and splices the result as a managed block into each
configured harness's *global* instruction file on the instance. Nothing is
ever written into the checkout. Per-session task text goes to
`~/sessions/<name>/TASK.md`, a sibling of the checkout that `checkpointCmd`
never sees.

**Tech Stack:** Go, pkl (schema + `pkl-go` codegen), cobra, huh, SSH via
`internal/reconcile`.

**Spec:** `docs/superpowers/specs/2026-09-15-agent-context-in-sessions-design.md`

## Global Constraints

- **Tests run through nix.** `nix develop --command go test ./...`. NEVER
  bare `go test` — the toolchain comes from the flake and is not otherwise
  on PATH.
- **TDD.** Write the failing test, watch it fail, then make it pass.
- **Commit style:** classic — imperative subject of 50 characters or fewer,
  capitalised, no trailing period, no `type:` prefix. Body wrapped at 72
  columns, explaining why rather than how. **Never an AI co-author
  trailer.**
- **Comments explain why, not what.** Match the density of the file you are
  in; `internal/lifecycle/session.go` is the reference.
- **Shell out to external tools** (`git`, `bd`, `sops`) rather than linking
  them.
- **Add no new Go dependencies.** Compare with `reflect.DeepEqual` or
  `slices.Equal`, which is what this repo's 58 existing comparisons use.
  `go-cmp` is not in go.mod and must not be added.
- **Nothing is written into the session checkout.** No creating
  `AGENTS.md`, no appending to `CLAUDE.md`, no `git update-index
  --skip-worktree`, no per-session file inside the working tree.
- **A harness cloudlab cannot reach is named, never silently skipped.**

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/Config.pkl` | declares `instructions` |
| `internal/config/write.go` | registers the field: constant, order, kind |
| `internal/config/read.go` | `fieldValue` case so `ReadValues` sees it |
| `internal/config/instructions.go` | resolve declared paths against their own file's directory |
| `internal/agentcontext/render.go` | cloudlab's block + user files, and the managed-block splice |
| `internal/agentcontext/harness.go` | `agents` entry to global path; the cursor gap |
| `internal/agentcontext/write.go` | write the spliced block over an SSH client |
| `internal/reconcile/reconcile.go` | call it on `up`/`provision` |
| `internal/lifecycle/start.go` | call it at session start; write `TASK.md` |
| `cmd/run_session.go` | `--task` / `--issue` |
| `internal/wizard/wizard.go` | one personal question |
| `docs/config.md` | document the field |

---

### Task 1: Declare and register the `instructions` field

Registration is the load-bearing half. `declaredFields` filters through
`KnownField`, which reads `fieldKinds` — so a field in the schema but not
in `fieldKinds` is invisible to `ReadValues`, and `cloudlab init` in edit
mode would silently drop the line. The test in Step 1 is that regression.

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify: `internal/config/write.go` (constants, `fieldOrder`, `fieldKinds`)
- Modify: `internal/config/read.go` (`fieldValue`)
- Modify: `docs/config.md`
- Test: `internal/config/write_test.go`

**Interfaces:**
- Produces: `config.FieldInstructions = "instructions"`, a `Listing<String>`
  rendered as a pkl listing exactly like `packages`.

- [ ] **Step 1: Write the failing round-trip test**

Add to `internal/config/write_test.go`:

```go
// A config declaring instructions must survive ReadValues then Render
// unchanged. declaredFields filters through KnownField, so a field added
// to Config.pkl but not to fieldKinds is invisible here and `cloudlab
// init` would drop the line when it rewrites the file.
func TestReadValues_KeepsInstructions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudlab.pkl")
	rendered, err := Render(Values{
		FieldRegion:       "nyc3",
		FieldSize:         "s-1vcpu-1gb",
		FieldTemplate:     "docker",
		FieldInstructions: []string{"docs/workflow.md", "docs/extra.md"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadValues() error = %v\nrendered:\n%s", err, rendered)
	}
	want := []string{"docs/workflow.md", "docs/extra.md"}
	if !reflect.DeepEqual(want, got[FieldInstructions]) {
		t.Errorf("instructions round trip = %#v, want %#v", got[FieldInstructions], want)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `nix develop --command go test ./internal/config/ -run TestReadValues_KeepsInstructions -v`

Expected: FAIL — `undefined: FieldInstructions`.

- [ ] **Step 3: Declare the field in the schema**

In `internal/config/Config.pkl`, after the `agents` declaration and before
`flakes`:

```pkl
/// Markdown files whose contents are delivered to every configured
/// coding agent on the instance.
///
/// Paths are relative to the file that declares them, so a base.pkl
/// entry resolves against ~/.config/cloudlab and a project entry
/// against the repository root. Merges additively like `packages`:
/// base's files are delivered before this file's.
///
/// cloudlab never parses the content. What a workflow says, and which
/// harness's vocabulary it is written in, is the author's business.
instructions: Listing<String> = new Listing {}
```

- [ ] **Step 4: Regenerate the Go bindings**

Run: `nix develop --command go generate ./internal/config/`

Expected: `internal/config/Config.pkl.go` gains an `Instructions []string`
field. `Resolved` embeds `Config`, so callers reach it with no further
change.

- [ ] **Step 5: Register the field**

In `internal/config/write.go`, add the constant after `FieldAgents`:

```go
	FieldAgents       = "agents"
	FieldInstructions = "instructions"
	FieldFlakes       = "flakes"
```

Add to `fieldOrder`, in Config.pkl's own declaration order — after
`FieldAgents`, before `FieldFlakes`:

```go
	FieldAgents,
	FieldInstructions,
	FieldFlakes,
```

Add to `fieldKinds`, which is what `KnownField` reads:

```go
	FieldInstructions: kindStringSlice,
```

Use whatever the existing constant for `[]string` is called — `FieldPackages`
and `FieldSSHKeys` already use it; copy that spelling rather than inventing
one.

- [ ] **Step 6: Teach `fieldValue` to read it**

In `internal/config/read.go`, in the `switch name` of `fieldValue`, beside
the `FieldPackages` case:

```go
	case FieldInstructions:
		return derefStringSlice(c.Instructions)
```

Match the existing `FieldPackages` case exactly — if it does not use a
`derefStringSlice` helper, do whatever it does instead. The two are the
same shape and must stay so.

- [ ] **Step 7: Run the test and the package suite**

Run: `nix develop --command go test ./internal/config/ -v`

Expected: PASS, including `TestReadValues_KeepsInstructions` and the
existing `TestRender_RoundTripsThroughResolve`.

- [ ] **Step 8: Document the field**

In `docs/config.md`, add a row to the field table after `agents`:

```markdown
| `instructions` | `Listing<String>` | No | empty | Markdown files delivered to every configured coding agent on the instance. Paths are relative to the declaring file; merges additively like `packages`. |
```

- [ ] **Step 9: Commit**

```bash
git add internal/config docs/config.md
git commit -m "$(cat <<'EOF'
Add an instructions field to the config schema

Names Markdown files to deliver to the coding agents on an instance.
A Listing so it merges additively like packages, which gives the
personal/project split without a new merge rule.

Registered in fieldKinds as well as the schema. KnownField reads that
map and declaredFields filters through it, so a field declared only in
Config.pkl is invisible to ReadValues -- and cloudlab init in edit mode
would drop the line when it rewrote the file.
EOF
)"
```

---

### Task 2: Resolve declared instruction paths

pkl's merge produces one list with no record of which file contributed
which entry, so the merged `Resolved.Instructions` cannot be resolved
against the right directory. Read each file's declared values separately
instead, with the machinery `ReadValues` already provides.

**Files:**
- Create: `internal/config/instructions.go`
- Test: `internal/config/instructions_test.go`

**Interfaces:**
- Consumes: `config.ReadValues`, `config.FieldInstructions` (Task 1),
  `resolveBasePath` (existing, unexported, same package).
- Produces: `func InstructionFiles(ctx context.Context, projectPath string)
  ([]string, error)` — absolute paths, base's before the project's, in
  declaration order. Returns an error naming any path that does not exist.

- [ ] **Step 1: Write the failing test**

Create `internal/config/instructions_test.go`:

```go
package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Each file's paths resolve against its own directory, and base's come
// first. A merged Config cannot express this: pkl concatenates the two
// listings and nothing records which file contributed which entry.
func TestInstructionFiles_ResolvesAgainstTheDeclaringFile(t *testing.T) {
	home := t.TempDir()
	baseDir := filepath.Join(home, "config")
	projectDir := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(baseDir, "instructions"))
	mustMkdirAll(t, filepath.Join(projectDir, "docs"))

	mustWrite(t, filepath.Join(baseDir, "instructions", "mine.md"), "personal\n")
	mustWrite(t, filepath.Join(projectDir, "docs", "theirs.md"), "project\n")

	basePath := filepath.Join(baseDir, "base.pkl")
	mustWrite(t, basePath, renderFor(t, Values{
		FieldInstructions: []string{"instructions/mine.md"},
	}))
	projectPath := filepath.Join(projectDir, "cloudlab.pkl")
	mustWrite(t, projectPath, renderFor(t, Values{
		FieldRegion:       "nyc3",
		FieldSize:         "s-1vcpu-1gb",
		FieldTemplate:     "docker",
		FieldBasePath:     basePath,
		FieldInstructions: []string{"docs/theirs.md"},
	}))

	got, err := InstructionFiles(context.Background(), projectPath)
	if err != nil {
		t.Fatalf("InstructionFiles() error = %v", err)
	}
	want := []string{
		filepath.Join(baseDir, "instructions", "mine.md"),
		filepath.Join(projectDir, "docs", "theirs.md"),
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("InstructionFiles() = %#v, want %#v", got, want)
	}
}

// A silently absent workflow is the failure this design exists to
// prevent, so a missing file stops the flow and names the path.
func TestInstructionFiles_MissingFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "cloudlab.pkl")
	mustWrite(t, projectPath, renderFor(t, Values{
		FieldRegion:       "nyc3",
		FieldSize:         "s-1vcpu-1gb",
		FieldTemplate:     "docker",
		FieldInstructions: []string{"docs/gone.md"},
	}))

	_, err := InstructionFiles(context.Background(), projectPath)
	if err == nil {
		t.Fatal("InstructionFiles() error = nil, want an error naming the path")
	}
	if !strings.Contains(err.Error(), "docs/gone.md") {
		t.Errorf("error %q does not name the missing path", err)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func renderFor(t *testing.T, v Values) string {
	t.Helper()
	s, err := Render(v)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return s
}
```

If helpers of these names already exist in the package's tests, use those
instead of redeclaring them — the compiler will say so.

- [ ] **Step 2: Run it and watch it fail**

Run: `nix develop --command go test ./internal/config/ -run TestInstructionFiles -v`

Expected: FAIL — `undefined: InstructionFiles`.

- [ ] **Step 3: Implement it**

Create `internal/config/instructions.go`:

```go
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// InstructionFiles returns the absolute path of every Markdown file the
// config chain declares, base's before the project's, each in its own
// declaration order.
//
// Read per file rather than off a merged Config, because merging is what
// destroys the information this needs. pkl concatenates the two listings
// into one, and a relative path in the result cannot say whether it meant
// ~/.config/cloudlab or the repository root.
//
// A missing file is an error rather than a warning. A workflow that
// silently fails to arrive is the failure the whole feature exists to
// prevent, and unlike an absent optional credential there is no degraded
// mode worth continuing into.
func InstructionFiles(ctx context.Context, projectPath string) ([]string, error) {
	project, err := ReadValues(ctx, projectPath)
	if err != nil {
		return nil, err
	}

	var override *string
	if s, ok := project[FieldBasePath].(string); ok {
		override = &s
	}

	var files []string
	basePath, err := resolveBasePath(projectPath, override)
	// A base that does not resolve is not this function's business: it is
	// either absent, which is ordinary, or malformed, which Resolve
	// reports with better context than a list of paths could.
	if err == nil && basePath != "" {
		base, berr := ReadValues(ctx, basePath)
		if berr == nil {
			resolved, rerr := resolveAgainst(filepath.Dir(basePath), base)
			if rerr != nil {
				return nil, rerr
			}
			files = append(files, resolved...)
		}
	}

	resolved, err := resolveAgainst(filepath.Dir(projectPath), project)
	if err != nil {
		return nil, err
	}
	return append(files, resolved...), nil
}

// resolveAgainst turns one file's declared instructions into absolute
// paths rooted at dir, failing on the first that does not exist.
func resolveAgainst(dir string, v Values) ([]string, error) {
	declared, ok := v[FieldInstructions].([]string)
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(declared))
	for _, rel := range declared {
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, rel)
		}
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("instructions: %s: %w", rel, err)
		}
		out = append(out, path)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `nix develop --command go test ./internal/config/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "$(cat <<'EOF'
Resolve instruction paths against their own file

A path in base.pkl means something relative to ~/.config/cloudlab and
one in cloudlab.pkl something relative to the repository, but pkl
concatenates the two listings and the merged result cannot say which
file an entry came from. Read each file's declared values separately
instead, which ReadValues already supports.

A missing file fails rather than warns. A workflow that silently does
not arrive is the failure this feature exists to prevent.
EOF
)"
```

---

### Task 3: Render the agent context block

**Files:**
- Create: `internal/agentcontext/render.go`
- Test: `internal/agentcontext/render_test.go`

**Interfaces:**
- Produces:
  - `const BeginMarker = "<!-- cloudlab:begin -->"`
  - `const EndMarker = "<!-- cloudlab:end -->"`
  - `func Render(files []string) (string, error)` — cloudlab's session
    facts followed by each file's contents verbatim, wrapped in the
    markers. `nil` files yields facts only.
  - `func Splice(existing, block string) string` — replaces an existing
    managed block or appends one, preserving everything outside it.

- [ ] **Step 1: Write the failing tests**

Create `internal/agentcontext/render_test.go`:

```go
package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With nothing configured a session still gets the facts. They are not
// opinion -- an agent that does not know commits are what survive will
// lose work -- so they are the floor, not an optional extra.
func TestRender_WithNoFilesIsFactsOnly(t *testing.T) {
	got, err := Render(nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, want := range []string{
		BeginMarker,
		EndMarker,
		"Do not push anywhere",
		"Commits are what survive",
		"../TASK.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q:\n%s", want, got)
		}
	}
}

// User content is transported, never interpreted: it arrives byte for
// byte, in order, after cloudlab's own block.
func TestRender_AppendsFilesVerbatimInOrder(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.md")
	second := filepath.Join(dir, "b.md")
	write(t, first, "# Mine\nrun `refactor:scan`\n")
	write(t, second, "# Theirs\n")

	got, err := Render([]string{first, second})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(got, "run `refactor:scan`") {
		t.Errorf("user content not delivered verbatim:\n%s", got)
	}
	if strings.Index(got, "# Mine") > strings.Index(got, "# Theirs") {
		t.Error("files delivered out of order")
	}
	if strings.Index(got, "Commits are what survive") > strings.Index(got, "# Mine") {
		t.Error("user content came before cloudlab's block")
	}
	if !strings.HasSuffix(strings.TrimSpace(got), EndMarker) {
		t.Errorf("block not closed by the end marker:\n%s", got)
	}
}

// Rewriting must not stack blocks, and must not disturb anything the
// user put in the file themselves.
func TestSplice_ReplacesInPlaceAndKeepsTheRest(t *testing.T) {
	existing := "# My own notes\nkeep me\n\n" +
		BeginMarker + "\nold content\n" + EndMarker + "\n\ntrailing\n"

	got := Splice(existing, BeginMarker+"\nnew content\n"+EndMarker)

	if strings.Contains(got, "old content") {
		t.Errorf("old block survived:\n%s", got)
	}
	if strings.Count(got, BeginMarker) != 1 {
		t.Errorf("block duplicated:\n%s", got)
	}
	for _, want := range []string{"keep me", "trailing", "new content"} {
		if !strings.Contains(got, want) {
			t.Errorf("Splice() lost %q:\n%s", want, got)
		}
	}
}

func TestSplice_AppendsWhenAbsent(t *testing.T) {
	got := Splice("# Theirs\n", BeginMarker+"\nblock\n"+EndMarker)
	if !strings.HasPrefix(got, "# Theirs") {
		t.Errorf("existing content not preserved first:\n%s", got)
	}
	if !strings.Contains(got, "block") {
		t.Errorf("block not appended:\n%s", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `nix develop --command go test ./internal/agentcontext/ -v`

Expected: FAIL — the package does not exist.

- [ ] **Step 3: Implement**

Create `internal/agentcontext/render.go`:

```go
// Package agentcontext builds the instructions cloudlab delivers to the
// coding agents on an instance.
//
// Two kinds of content, and only one is cloudlab's. cloudlab authors the
// session facts, because it is the only thing that knows them and they
// are true of every session. The workflow is the user's, named by the
// config's instructions field and transported verbatim -- shipping a
// workflow of cloudlab's own would push one harness's plugins on users
// running another.
package agentcontext

import (
	"fmt"
	"os"
	"strings"
)

// The managed block's delimiters. Everything between them belongs to
// cloudlab and is replaced wholesale on every write; everything outside
// belongs to whoever put it there and is preserved.
const (
	BeginMarker = "<!-- cloudlab:begin -->"
	EndMarker   = "<!-- cloudlab:end -->"
)

// sessionFacts is what only cloudlab can say. The closing paragraph
// matters as much as the rest: these files load before a project's own,
// so without it cloudlab's generic advice reads as outranking the
// repository's actual conventions.
const sessionFacts = `# Working in a cloudlab session

You are a coding agent on an ephemeral cloud VM created by cloudlab.
Your checkout is at ~/sessions/<name>/<repo> on branch cloudlab/<name>.
Run ` + "`pwd`" + ` and ` + "`git branch --show-current`" + ` for the actual values.

- **Do not push anywhere.** You have no credentials, no GitHub access
  and no network git remote.
- **Commits are what survive.** Work returns to the user's machine via
  ` + "`cloudlab session pull`" + ` and ` + "`cloudlab session merge`" + `, run from their
  end. Uncommitted changes are swept into a checkpoint commit, so commit
  deliberately rather than relying on that.
- **Anything untracked gets committed** by that checkpoint (` + "`git add -A`" + `).
  Leave no scratch files, archives or databases in the working tree.
- The VM is destroyed on ` + "`cloudlab down`" + `. Nothing outside the repository
  survives.

## What this session is for

1. If ` + "`../TASK.md`" + ` exists beside your checkout, read it -- that is what
   you were started for.
2. Otherwise, if the repository uses beads, check ` + "`bd list --status in_progress`" + `,
   then ` + "`bd ready`" + `.
3. Otherwise, ask before starting work.

The repository's own AGENTS.md, CLAUDE.md or CONTRIBUTING.md still apply
and win wherever they disagree with this file.
`

// Render builds the managed block: cloudlab's session facts, then each
// named file's contents verbatim.
func Render(files []string) (string, error) {
	var b strings.Builder
	b.WriteString(BeginMarker)
	b.WriteString("\n")
	b.WriteString(sessionFacts)

	for _, path := range files {
		content, err := os.ReadFile(path) // #nosec G304 -- the user named this file in their own config
		if err != nil {
			return "", fmt.Errorf("reading instructions %s: %w", path, err)
		}
		b.WriteString("\n")
		b.Write(content)
		if !strings.HasSuffix(string(content), "\n") {
			b.WriteString("\n")
		}
	}

	b.WriteString(EndMarker)
	b.WriteString("\n")
	return b.String(), nil
}

// Splice puts block into existing, replacing a previous managed block if
// there is one and appending it otherwise.
//
// Replacing rather than appending is what makes repeated writes safe:
// reconcile runs on every up and provision, and session start writes
// again, so an appending implementation would grow the file without
// bound.
func Splice(existing, block string) string {
	start := strings.Index(existing, BeginMarker)
	end := strings.Index(existing, EndMarker)
	if start >= 0 && end > start {
		tail := existing[end+len(EndMarker):]
		return existing[:start] + strings.TrimSuffix(block, "\n") + tail
	}

	if existing == "" {
		return block
	}
	if !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return existing + "\n" + block
}
```

- [ ] **Step 4: Run the tests**

Run: `nix develop --command go test ./internal/agentcontext/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentcontext
git commit -m "$(cat <<'EOF'
Render the instructions an instance's agents read

cloudlab authors the session facts, because it is the only thing that
knows them and they hold for every session. The workflow is the user's
and is transported verbatim -- shipping one of cloudlab's own would push
one harness's plugins on users running another.

Splice replaces a previous block rather than appending. reconcile writes
on every up and provision and session start writes again, so appending
would grow the file without bound.
EOF
)"
```

---

### Task 4: Map harnesses to their global instruction files

**Files:**
- Create: `internal/agentcontext/harness.go`
- Test: `internal/agentcontext/harness_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func GlobalPaths(agents []string) (paths []string, unreachable
  []string)` — the remote path for each agent that has one, and the names
  of those that do not. Paths are returned relative to the remote home
  with no leading `~/`, for the caller to place.

- [ ] **Step 1: Write the failing test**

Create `internal/agentcontext/harness_test.go`:

```go
package agentcontext

import (
	"reflect"
	"testing"
)

func TestGlobalPaths_MapsEachHarnessToItsGlobalFile(t *testing.T) {
	paths, unreachable := GlobalPaths([]string{"claude", "codex", "opencode", "copilot", "pi"})
	want := []string{
		".claude/CLAUDE.md",
		".codex/AGENTS.md",
		".config/opencode/AGENTS.md",
		".copilot/copilot-instructions.md",
		".pi/agent/AGENTS.md",
	}
	if !reflect.DeepEqual(want, paths) {
		t.Errorf("GlobalPaths() paths = %#v, want %#v", paths, want)
	}
	if len(unreachable) != 0 {
		t.Errorf("GlobalPaths() unreachable = %v, want none", unreachable)
	}
}

// Cursor's cross-project rules live in GUI User Rules, so there is no
// file to write. Reaching it would mean writing into the checkout, which
// this design refuses -- so it is named rather than silently skipped: an
// instruction that never arrived looks exactly like one that was ignored.
func TestGlobalPaths_NamesCursorAsUnreachable(t *testing.T) {
	paths, unreachable := GlobalPaths([]string{"claude", "cursor"})
	if !reflect.DeepEqual([]string{".claude/CLAUDE.md"}, paths) {
		t.Errorf("GlobalPaths() paths = %#v", paths)
	}
	if !reflect.DeepEqual([]string{"cursor"}, unreachable) {
		t.Errorf("GlobalPaths() unreachable = %#v", unreachable)
	}
}

func TestGlobalPaths_IgnoresUnknownNames(t *testing.T) {
	paths, unreachable := GlobalPaths([]string{"nonesuch"})
	if len(paths) != 0 || len(unreachable) != 0 {
		t.Errorf("GlobalPaths() = %v, %v, want empty", paths, unreachable)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `nix develop --command go test ./internal/agentcontext/ -run TestGlobalPaths -v`

Expected: FAIL — `undefined: GlobalPaths`.

- [ ] **Step 3: Implement**

Create `internal/agentcontext/harness.go`:

```go
package agentcontext

// globalPaths maps a config `agents` entry to the file that harness reads
// for instructions that apply to every project, relative to the remote
// home directory.
//
// The global slot rather than anything in or above the checkout, because
// it is the only mechanism the harnesses agree on. Per-directory discovery
// is not portable: Claude Code and opencode walk upward from the working
// directory, while Codex begins its chain at the git repository root and
// walks down, so a file above the checkout reaches two of them and is
// invisible to the third.
//
// Keep in step with Config.pkl's agents union. A test reads the schema and
// fails if the two sets drift.
var globalPaths = map[string]string{
	"claude":   ".claude/CLAUDE.md",
	"codex":    ".codex/AGENTS.md",
	"opencode": ".config/opencode/AGENTS.md",
	"copilot":  ".copilot/copilot-instructions.md",
	"pi":       ".pi/agent/AGENTS.md",
}

// unreachableHarnesses have no global instruction file to write.
//
// Cursor's CLI reads AGENTS.md and CLAUDE.md at the project root and
// .cursor/rules, but cross-project instructions live in User Rules, set in
// the GUI. Delivering to it would mean writing into the checkout, which
// this design refuses.
var unreachableHarnesses = map[string]bool{
	"cursor": true,
}

// GlobalPaths returns the remote path for each named harness that has one,
// and the names of those that do not.
//
// Both results follow the caller's order rather than the map's, so a
// rendered warning and a written file list read the way the config does.
func GlobalPaths(agents []string) (paths []string, unreachable []string) {
	for _, a := range agents {
		if p, ok := globalPaths[a]; ok {
			paths = append(paths, p)
			continue
		}
		if unreachableHarnesses[a] {
			unreachable = append(unreachable, a)
		}
	}
	return paths, unreachable
}
```

- [ ] **Step 4: Add the drift test**

`internal/wizard/wizard_test.go` already has a test that reads
Config.pkl's `agents` union and fails if the wizard's option list drifts.
Find it (`grep -rn "agents union\|drift" internal/wizard/`), read how it
extracts the union, and add the equivalent to
`internal/agentcontext/harness_test.go`:

```go
// Every name Config.pkl accepts must be either mapped or explicitly
// unreachable. A new harness added to the schema and forgotten here would
// silently receive no instructions.
func TestGlobalPaths_CoversEveryAgentInTheSchema(t *testing.T) {
	for _, name := range agentsUnionFromSchema(t) {
		if _, ok := globalPaths[name]; ok {
			continue
		}
		if unreachableHarnesses[name] {
			continue
		}
		t.Errorf("agent %q is in Config.pkl but neither mapped nor listed unreachable", name)
	}
}
```

Copy `agentsUnionFromSchema` from the wizard test's equivalent helper,
adjusting the relative path to `Config.pkl` for this package's directory.

- [ ] **Step 5: Run the tests**

Run: `nix develop --command go test ./internal/agentcontext/ -v`

Expected: PASS, all four tests.

- [ ] **Step 6: Commit**

```bash
git add internal/agentcontext
git commit -m "$(cat <<'EOF'
Map each harness to its global instruction file

The global slot is the only mechanism the harnesses agree on.
Per-directory discovery is not portable: Claude Code and opencode walk
upward from the working directory, while Codex starts at the git
repository root and walks down, so a file above the checkout reaches two
and is invisible to the third.

Cursor has no such file -- its cross-project rules are GUI User Rules --
so it is reported unreachable rather than skipped. An instruction that
never arrived looks exactly like one that was ignored.
EOF
)"
```

---

### Task 5: Write the block to an instance

**Files:**
- Create: `internal/agentcontext/write.go`
- Test: `internal/agentcontext/write_test.go`

**Interfaces:**
- Consumes: `Render`, `Splice`, `GlobalPaths` (Tasks 3, 4).
- Produces: `func Write(rw RemoteFiles, agents []string, files []string)
  (unreachable []string, err error)` and the interface it takes:

```go
type RemoteFiles interface {
	ReadFile(path string) (string, error)
	WriteFile(path, content string) error
}
```

  `*reconcile.Client` does not satisfy this as written — Task 6 adapts it.
  The interface exists so this is testable without SSH.

- [ ] **Step 1: Write the failing test**

Create `internal/agentcontext/write_test.go`:

```go
package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRemote is an in-memory RemoteFiles. The SSH client is the one thing
// no in-process harness here can stand in for, so everything above it is
// given nothing to decide.
type fakeRemote struct {
	files map[string]string
}

func (f *fakeRemote) ReadFile(path string) (string, error) {
	return f.files[path], nil
}

func (f *fakeRemote) WriteFile(path, content string) error {
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = content
	return nil
}

func TestWrite_WritesOnePerConfiguredHarness(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "w.md")
	if err := os.WriteFile(workflow, []byte("# Workflow\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	remote := &fakeRemote{}
	unreachable, err := Write(remote, []string{"claude", "codex"}, []string{workflow})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(unreachable) != 0 {
		t.Errorf("unreachable = %v, want none", unreachable)
	}
	for _, p := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md"} {
		if !strings.Contains(remote.files[p], "# Workflow") {
			t.Errorf("%s did not get the workflow:\n%s", p, remote.files[p])
		}
		if !strings.Contains(remote.files[p], "Commits are what survive") {
			t.Errorf("%s did not get the session facts", p)
		}
	}
}

// reconcile writes on every up and provision, and session start writes
// again. Twice must equal once.
func TestWrite_IsIdempotent(t *testing.T) {
	remote := &fakeRemote{}
	if _, err := Write(remote, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	once := remote.files[".claude/CLAUDE.md"]
	if _, err := Write(remote, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := remote.files[".claude/CLAUDE.md"]; got != once {
		t.Errorf("second write differed:\nfirst:\n%s\nsecond:\n%s", once, got)
	}
}

func TestWrite_PreservesContentTheUserPut There(t *testing.T) {
	remote := &fakeRemote{files: map[string]string{
		".claude/CLAUDE.md": "# Mine\nkeep me\n",
	}}
	if _, err := Write(remote, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remote.files[".claude/CLAUDE.md"], "keep me") {
		t.Errorf("user content lost:\n%s", remote.files[".claude/CLAUDE.md"])
	}
}

func TestWrite_ReportsUnreachableHarnesses(t *testing.T) {
	remote := &fakeRemote{}
	unreachable, err := Write(remote, []string{"cursor"}, nil)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(unreachable) != 1 || unreachable[0] != "cursor" {
		t.Errorf("unreachable = %v, want [cursor]", unreachable)
	}
}
```

Fix the typo in the third test's name when you paste it —
`TestWrite_PreservesContentTheUserPutThere`.

- [ ] **Step 2: Run it and watch it fail**

Run: `nix develop --command go test ./internal/agentcontext/ -run TestWrite -v`

Expected: FAIL — `undefined: Write`.

- [ ] **Step 3: Implement**

Create `internal/agentcontext/write.go`:

```go
package agentcontext

import "fmt"

// RemoteFiles is the part of an SSH client this package needs.
//
// An interface rather than *reconcile.Client so the splice, the harness
// mapping and the idempotence are all exercised without a network. The
// client is the one thing no in-process harness can stand in for, so it
// is given nothing to decide.
type RemoteFiles interface {
	// ReadFile returns the file's contents, or "" if it does not exist.
	ReadFile(path string) (string, error)
	WriteFile(path, content string) error
}

// Write puts the rendered block into each configured harness's global
// instruction file, and reports the harnesses that have none.
//
// Paths are relative to the remote home directory; the caller's WriteFile
// places them.
func Write(rw RemoteFiles, agents []string, files []string) (unreachable []string, err error) {
	paths, unreachable := GlobalPaths(agents)
	if len(paths) == 0 {
		return unreachable, nil
	}

	block, err := Render(files)
	if err != nil {
		return unreachable, err
	}

	for _, path := range paths {
		existing, err := rw.ReadFile(path)
		if err != nil {
			return unreachable, fmt.Errorf("reading %s on the instance: %w", path, err)
		}
		if err := rw.WriteFile(path, Splice(existing, block)); err != nil {
			return unreachable, fmt.Errorf("writing %s on the instance: %w", path, err)
		}
	}
	return unreachable, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `nix develop --command go test ./internal/agentcontext/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentcontext
git commit -m "$(cat <<'EOF'
Write the instruction block to an instance

Takes a small RemoteFiles interface rather than *reconcile.Client, so
the splice, the harness mapping and the idempotence are exercised
without a network. The client is the one thing no in-process harness
here can stand in for, so it is given nothing to decide.
EOF
)"
```

---

### Task 6: Deliver on `up`, `provision` and session start

Both call sites run the same idempotent write. `reconcile` alone is not
enough: it runs on `up` and `provision`, while `seedSession` only borrows
`reconcile.Connect` for a client. A user who edits their workflow and runs
`session start` would otherwise get the instructions as they stood at the
last `up`, silently.

**Files:**
- Create: `internal/agentcontext/client.go` (the `*reconcile.Client` adapter)
- Modify: `internal/reconcile/reconcile.go` (inside `Reconcile`, after the
  client is connected)
- Modify: `internal/lifecycle/start.go` (inside `seedSession`)
- Test: `internal/agentcontext/client_test.go`

**Interfaces:**
- Consumes: `agentcontext.Write`, `agentcontext.RemoteFiles` (Task 5),
  `config.InstructionFiles` (Task 2), `reconcile.Client.Run`,
  `reconcile.Client.WriteFile`.
- Produces: `func Remote(run func(string) (string, error), write func(string, string) error) RemoteFiles`.

- [ ] **Step 1: Write the failing adapter test**

Create `internal/agentcontext/client_test.go`:

```go
package agentcontext

import (
	"errors"
	"strings"
	"testing"
)

// A file that is not there yet is the ordinary case on a fresh instance,
// and must read as empty rather than as an error -- otherwise the very
// first write fails.
func TestRemote_TreatsAMissingFileAsEmpty(t *testing.T) {
	r := Remote(
		func(cmd string) (string, error) { return "", errors.New("exit status 1") },
		func(path, content string) error { return nil },
	)
	got, err := r.ReadFile(".claude/CLAUDE.md")
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("ReadFile() = %q, want empty", got)
	}
}

func TestRemote_ReadsThroughTheRunner(t *testing.T) {
	var asked string
	r := Remote(
		func(cmd string) (string, error) { asked = cmd; return "contents", nil },
		func(path, content string) error { return nil },
	)
	got, err := r.ReadFile(".codex/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != "contents" {
		t.Errorf("ReadFile() = %q", got)
	}
	if !strings.Contains(asked, ".codex/AGENTS.md") {
		t.Errorf("command %q does not name the path", asked)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `nix develop --command go test ./internal/agentcontext/ -run TestRemote -v`

Expected: FAIL — `undefined: Remote`.

- [ ] **Step 3: Implement the adapter**

Create `internal/agentcontext/client.go`:

```go
package agentcontext

import (
	"github.com/jskswamy/cloudlab/internal/shellcmd"
)

// Remote adapts an SSH client's Run and WriteFile to RemoteFiles.
//
// Taking the two functions rather than the client keeps this package off
// internal/reconcile, which already depends on internal/config -- and
// leaves the adapter itself testable without a connection.
func Remote(run func(string) (string, error), write func(string, string) error) RemoteFiles {
	return remote{run: run, write: write}
}

type remote struct {
	run   func(string) (string, error)
	write func(string, string) error
}

// ReadFile returns "" when the file is not there. On a fresh instance
// none of these files exist, so an error would fail the first write.
// Every other failure also reads as empty, which is the safe direction:
// the block is rewritten wholesale, and the cost of misreading a present
// file as absent is losing hand-added content, not a broken instruction.
func (r remote) ReadFile(path string) (string, error) {
	out, err := r.run(shellcmd.LoginShell("cat " + shellcmd.Quote("$HOME/"+path) + " 2>/dev/null || true"))
	if err != nil {
		return "", nil
	}
	return out, nil
}

func (r remote) WriteFile(path, content string) error {
	return r.write("$HOME/"+path, content)
}
```

Check `internal/reconcile/ssh.go:240`'s `WriteFile` before finishing this:
if it does not expand `$HOME`, pass the absolute remote home instead —
`seedBeads` and `RemoteRepoPath` show how the user's home is composed
elsewhere, and `shellcmd.Quote` must not be applied to a path you want the
shell to expand. Adjust the test to match whichever you choose.

- [ ] **Step 4: Call it from `Reconcile`**

In `internal/reconcile/reconcile.go`, after the client is connected and
before the existing provisioning work, add:

```go
	if err := writeAgentContext(ctx, client, cfg, cloudlabPath); err != nil {
		return err
	}
```

and below `Reconcile`, in the same file:

```go
// writeAgentContext delivers cloudlab's session facts and the user's
// instructions files to every configured harness.
//
// Here as well as at session start, deliberately. This runs on up and
// provision; seedSession runs the same write again because instructions
// names files the user edits between sessions, and delivering the version
// current at the last up -- silently -- is the failure the feature exists
// to prevent.
func writeAgentContext(ctx context.Context, client *Client, cfg config.Resolved, cloudlabPath string) error {
	files, err := config.InstructionFiles(ctx, cloudlabPath)
	if err != nil {
		return err
	}
	unreachable, err := agentcontext.Write(
		agentcontext.Remote(client.Run, client.WriteFile),
		cfg.Agents,
		files,
	)
	if err != nil {
		return err
	}
	for _, name := range unreachable {
		provider.ReportWarning(ctx, "instructions: "+name+" has no global instruction file, so cloudlab cannot deliver to it — put them in that tool's own settings")
	}
	return nil
}
```

Confirm `cfg.Agents`' exact type and spelling against the generated
`Config.pkl.go`; if it is `[]string` this compiles as written.

- [ ] **Step 5: Call it from `seedSession`**

In `internal/lifecycle/start.go`, inside `seedSession`, after the checkout
succeeds and before `seedBeads`:

```go
	// Again here, not only in reconcile. instructions names files the
	// user edits between sessions, so a session started after an edit must
	// carry the edit rather than whatever the last up delivered.
	if err := writeAgentContext(ctx, client, localRepo); err != nil {
		provider.ReportWarning(ctx, "instructions: "+err.Error())
	}
```

`seedSession` has no `config.Resolved` in scope. Add a small helper in
`internal/lifecycle` that resolves the repository's config path the way
other lifecycle code does (`grep -rn "config.Resolve" internal/lifecycle/`
shows the established pattern) and calls the same
`agentcontext.Write`. A warning rather than an error: the session itself
is still usable, and a failed instruction write must not strand a created
instance — the same reasoning `seedBeads` applies.

- [ ] **Step 6: Run the full suite**

Run: `nix develop --command go test ./...`

Expected: PASS. This is a long run (lifecycle and provisioning each take
minutes).

- [ ] **Step 7: Commit**

```bash
git add internal/agentcontext internal/reconcile internal/lifecycle
git commit -m "$(cat <<'EOF'
Deliver agent instructions on up and at session start

One idempotent write, two call sites. reconcile alone is not enough: it
runs on up and provision, while seedSession only borrows
reconcile.Connect for a client. instructions names files the user edits
between sessions, so a session started after an edit would otherwise
carry whatever the last up delivered, silently.

A failure at session start warns rather than returns. The session is
still usable, and a failed instruction write must not strand a created
instance -- the reasoning seedBeads already applies.
EOF
)"
```

---

### Task 7: `--task` and `--issue` on `session start`

**Files:**
- Create: `internal/lifecycle/task.go`
- Modify: `cmd/run_session.go` (the `start` subcommand)
- Test: `internal/lifecycle/task_test.go`

**Interfaces:**
- Consumes: `reconcile.Client.WriteFile`, `lifecycle.RemoteRepoPath`
  (existing).
- Produces: `func WriteTask(client *reconcile.Client, user, session, text string) error`
  writing `~/sessions/<session>/TASK.md`, and
  `func TaskText(task, issue string) (string, error)` building the content.

- [ ] **Step 1: Write the failing test**

Create `internal/lifecycle/task_test.go`:

```go
package lifecycle

import (
	"strings"
	"testing"
)

func TestTaskText_WritesFreeTextVerbatim(t *testing.T) {
	got, err := TaskText("Implement bv5; spec in docs/foo.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Implement bv5; spec in docs/foo.md") {
		t.Errorf("TaskText() = %q", got)
	}
}

// The ID and the command that expands it, not a copy of the issue. A copy
// is stale the moment anyone edits the issue, and the agent has bd on the
// instance.
func TestTaskText_ForAnIssueNamesItAndHowToReadIt(t *testing.T) {
	got, err := TaskText("", "cloudlab-bv5")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cloudlab-bv5", "bd show cloudlab-bv5"} {
		if !strings.Contains(got, want) {
			t.Errorf("TaskText() = %q, missing %q", got, want)
		}
	}
}

// A precedence rule nobody would remember is worse than a refusal.
func TestTaskText_RefusesBothFlags(t *testing.T) {
	_, err := TaskText("some text", "cloudlab-bv5")
	if err == nil {
		t.Fatal("TaskText() error = nil, want an error")
	}
	for _, want := range []string{"--task", "--issue"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestTaskText_WithNeitherReportsNothingToWrite(t *testing.T) {
	got, err := TaskText("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("TaskText() = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestTaskText -v`

Expected: FAIL — `undefined: TaskText`.

- [ ] **Step 3: Implement**

Create `internal/lifecycle/task.go`:

```go
package lifecycle

import (
	"errors"
	"fmt"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// TaskText builds what `~/sessions/<name>/TASK.md` should say, or "" when
// the user named no task.
//
// An issue contributes its ID and the command that expands it rather than
// a copy of its body: a copy is stale the moment anyone edits the issue,
// and the agent has bd on the instance.
func TaskText(task, issue string) (string, error) {
	if task != "" && issue != "" {
		return "", errors.New("pass --task or --issue, not both")
	}
	switch {
	case task != "":
		return "# What this session is for\n\n" + task + "\n", nil
	case issue != "":
		return fmt.Sprintf("# What this session is for\n\nIssue %s. Read it with `bd show %s`.\n", issue, issue), nil
	default:
		return "", nil
	}
}

// WriteTask puts text beside the session's checkout.
//
// A sibling of the checkout, never inside it. checkpointCmd's `git add -A`
// never looks here, so unlike .beads/ this needs no exclusion -- and an
// exclusion is a thing that can fail, which is why seedBeads abandons
// beads entirely when excludeBeadsCmd errors.
func WriteTask(client *reconcile.Client, user, session, text string) error {
	if text == "" {
		return nil
	}
	return client.WriteFile(SessionDir(user, session)+"/TASK.md", text)
}
```

`SessionDir` may not exist. `RemoteRepoPath(user, session, repoName)`
already composes `~/sessions/<session>/<repo>`; extract the parent as
`SessionDir(user, session)` in whichever file `RemoteRepoPath` lives in,
and have `RemoteRepoPath` call it so the two cannot drift.

- [ ] **Step 4: Run the tests**

Run: `nix develop --command go test ./internal/lifecycle/ -run 'TestTaskText|TestSessionDir' -v`

Expected: PASS.

- [ ] **Step 5: Thread it through `StartSession` and the command**

`StartSession` gains a `task` string parameter, passed to `WriteTask`
inside `seedSession` after the checkout. In `cmd/run_session.go`'s `start`
subcommand, register the flags:

```go
	cmd.Flags().String("task", "", "What this session is for, in your own words")
	cmd.Flags().String("issue", "", "The beads issue this session is for, e.g. cloudlab-bv5")
```

read them, call `lifecycle.TaskText(task, issue)` **before** any instance
work so the both-flags error costs nothing, and pass the result down.

With `--issue` and a repository that has beads, also mark the issue in
progress in the session's database after `seedBeads` succeeds, shelling
out to `bd` the way `internal/beads` already does. With no beads, warn
that the issue cannot be marked and continue — the file is written either
way.

Update the `start` subcommand's `Long` text to describe both flags.

- [ ] **Step 6: Run the full suite**

Run: `nix develop --command go test ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/lifecycle cmd
git commit -m "$(cat <<'EOF'
Let session start say what the session is for

--task takes free text and --issue a beads ID; both write TASK.md
beside the checkout, where checkpointCmd's git add -A never looks. A
sibling rather than an excluded file inside the tree: an exclusion can
fail, which is why seedBeads abandons beads outright when
excludeBeadsCmd errors.

--issue writes the ID and `bd show`, not a copy of the issue, which
would be stale the moment anyone edited it. Beads enhances rather than
gates: with no beads the file is still written.
EOF
)"
```

---

### Task 8: The wizard question

In-loop validation needs somewhere to live. The scripted prompter returns
one answer per field and cannot model re-asking, and `runInitFlow` looping
on it would spin forever. So the check is a validator carried on the
`Question` and wired into huh's own `Validate` hook, where re-asking is
already huh's job — keeping with "the forms are covered by the units
beneath them, not driven."

**Files:**
- Modify: `internal/wizard/wizard.go` (`Question`, `PersonalQuestions`)
- Modify: `cmd/init.go` and `cmd/prompt.go` (pass the directory, wire the hook)
- Test: `internal/wizard/wizard_test.go`

**Interfaces:**
- Consumes: `config.FieldInstructions` (Task 1).
- Produces: `Question.Validate func(string) error`, nil for every existing
  question, and `PersonalQuestions(baseDir string) []Question` — the
  signature gains the directory the answers will be written beside, so the
  instructions validator can resolve relative paths against it.

- [ ] **Step 1: Write the failing tests**

Add to `internal/wizard/wizard_test.go`:

```go
// Personal, so it is asked once when base.pkl is created and again only
// on [change] -- not on every init. A path has nothing to enumerate, so
// unlike sshKeys it is an ordinary question rather than its own step.
func TestPersonalQuestions_IncludesInstructions(t *testing.T) {
	var found *Question
	qs := PersonalQuestions(t.TempDir())
	for i := range qs {
		if qs[i].Field == config.FieldInstructions {
			found = &qs[i]
		}
	}
	if found == nil {
		t.Fatal("PersonalQuestions() has no instructions question")
	}
	if found.Kind != TextList {
		t.Errorf("Kind = %v, want TextList", found.Kind)
	}
	if found.Validate == nil {
		t.Error("the instructions question carries no validator")
	}
}

// Checked while the question is on screen. A path that is not there is a
// hard error inside config.Resolve, which is the flow's own read-back --
// deferring would fail the wizard at the finish line rather than defer it
// anywhere useful.
func TestPersonalQuestions_InstructionsValidatorChecksThePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := instructionsQuestion(t, dir).Validate

	if err := v("real.md"); err != nil {
		t.Errorf("Validate(%q) = %v, want nil", "real.md", err)
	}
	if err := v(""); err != nil {
		t.Errorf("Validate(\"\") = %v, want nil -- blank means none", err)
	}
	err := v("gone.md")
	if err == nil {
		t.Fatal("Validate(\"gone.md\") = nil, want an error")
	}
	if !strings.Contains(err.Error(), "gone.md") {
		t.Errorf("error %q does not name the path", err)
	}
}

// Every other question keeps the wizard's existing rule: region and size
// take typed slugs and are caught by the provider at up, not here.
func TestPersonalQuestions_OtherQuestionsAreNotValidated(t *testing.T) {
	for _, q := range PersonalQuestions(t.TempDir()) {
		if q.Field == config.FieldInstructions {
			continue
		}
		if q.Validate != nil {
			t.Errorf("question %q gained a validator", q.Field)
		}
	}
}

// Presets carry the project's shape. A path like docs/agent-workflow.md
// means nothing stamped into another repository, and a personal one
// belongs to the user rather than the shape -- the same reasoning that
// keeps sshKeys out.
func TestPresetShape_ExcludesInstructions(t *testing.T) {
	for _, f := range presetShape {
		if f == config.FieldInstructions {
			t.Error("a preset must not capture instructions")
		}
	}
}

func instructionsQuestion(t *testing.T, dir string) Question {
	t.Helper()
	for _, q := range PersonalQuestions(dir) {
		if q.Field == config.FieldInstructions {
			return q
		}
	}
	t.Fatal("no instructions question")
	return Question{}
}
```

Read `presetShape` at `internal/wizard/wizard.go` before running this — if
it is not a `[]string` of field names, assert against whatever shape it
actually has.

- [ ] **Step 2: Run them and watch them fail**

Run: `nix develop --command go test ./internal/wizard/ -run 'Instructions|PresetShape' -v`

Expected: FAIL — `PersonalQuestions` takes no argument and `Question` has
no `Validate`.

- [ ] **Step 3: Add the validator field and the question**

In `internal/wizard/wizard.go`, add to `Question` after `Placeholder`:

```go
	// Validate rejects an answer while the question is still on screen.
	//
	// Nil for almost every question, deliberately: region and size take
	// typed slugs that the provider catches at up, and checking them here
	// would need a token the first run does not have. It is set only where
	// the failure would otherwise land inside this flow's own read-back
	// rather than usefully later.
	Validate func(string) error
```

Change the signature and append the question:

```go
func PersonalQuestions(baseDir string) []Question {
	return []Question{
		// ... region and size unchanged ...
		{
			Field:       config.FieldInstructions,
			Title:       "Any instructions for your coding agents?",
			Description: "Markdown files delivered to every agent on the instance, relative to this file. Leave blank for none.",
			Kind:        TextList,
			Placeholder: "instructions/my-workflow.md",
			Validate: func(v string) error {
				if v == "" {
					return nil
				}
				path := v
				if !filepath.IsAbs(path) {
					path = filepath.Join(baseDir, v)
				}
				if _, err := os.Stat(path); err != nil {
					return fmt.Errorf("%s: no such file, relative to %s", v, baseDir)
				}
				return nil
			},
		},
	}
}
```

Extend `PersonalQuestions`' doc comment, which already explains why sshKeys
is absent and why region and size go unvalidated, with why this one does
not:

```go
// instructions is the one question checked here, against the rule the
// paragraph above sets out. The difference is where the failure lands: a
// wrong-but-valid region slug surfaces at up, long after this flow exited,
// whereas a path that is not there is a hard error inside config.Resolve
// -- which is this flow's own read-back. Deferring would not defer it
// anywhere useful; it would fail the wizard at the finish line with every
// question already answered.
```

- [ ] **Step 4: Wire the hook and fix the call sites**

Run `grep -rn "PersonalQuestions(" cmd/ internal/` and give each caller the
directory its answers are written beside — for `cmd/init.go` that is the
directory holding `base.pkl`, which the flow already computes in order to
write it.

In `cmd/prompt.go`, where a `Text`/`TextList` question becomes a huh field,
pass the validator through to huh's own `Validate`, so huh re-asks:

```go
	if q.Validate != nil {
		field = field.Validate(q.Validate)
	}
```

Match `cmd/prompt.go`'s actual builder variable and huh's `Validate`
signature for the field type in question — for a `TextList` rendered as one
comma-separated input, the validator has to run per entry after splitting,
not on the raw line.

- [ ] **Step 5: Run the tests**

Run: `nix develop --command go test ./internal/wizard/ ./cmd/ -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/wizard cmd
git commit -m "$(cat <<'EOF'
Ask about agent instructions in the wizard

One more personal question rather than a new flow, which puts it under
the existing "personal questions are asked once" rule: it appears on a
first run and on [change], never on a routine init.

Checked while it is on screen, against the rule region and size set. The
difference is where the failure lands -- a wrong slug surfaces at up,
whereas a path that is not there is a hard error inside the flow's own
read-back and would fail the wizard at the finish line. The check is a
validator on the Question, wired into huh's own hook, because re-asking
is huh's job and the scripted prompter cannot model it.

Presets do not capture it, for the reason they do not capture sshKeys.
EOF
)"
```

---

### Task 9: Dogfood it

Point this repository at its own workflow spec, which stops that document
being hardcoded policy and makes it one repository's choice.

**Files:**
- Modify: `cloudlab.pkl`
- Modify: `docs/superpowers/specs/2026-09-15-agent-context-in-sessions-design.md`
  (status line)

- [ ] **Step 1: Set the field**

In this repository's `cloudlab.pkl`:

```pkl
// Delivered to the agents on the instance. The workflow is this
// repository's choice, not cloudlab's default -- see the 2026-09-15
// agent-context spec for why cloudlab ships none of its own.
instructions {
  "docs/superpowers/specs/2026-09-14-session-development-workflow-design.md"
}
```

- [ ] **Step 2: Verify the config still resolves**

Run: `nix develop --command go test ./internal/config/ -v`

Expected: PASS — `examples_test.go` and the repository's own config load.

- [ ] **Step 3: Mark the spec implemented**

Change the spec's header line to `Status: implemented`, and add an "As
implemented" section recording anything the code decided that the design
did not say — the 2026-09-09 and 2026-09-13 specs both show the form.

- [ ] **Step 4: Run everything**

Run: `nix develop --command go test ./...`
Run: `nix develop --command pre-commit run --all-files`

Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add cloudlab.pkl docs/superpowers/specs
git commit -m "$(cat <<'EOF'
Point cloudlab at its own session workflow

The 2026-09-14 workflow was named in AGENTS.md, which reaches an agent
working on cloudlab and nothing else. Declaring it here delivers it to
the agents on the instance instead -- and makes it this repository's
choice rather than something cloudlab ships to everyone.
EOF
)"
```

---

## Self-review notes

**Spec coverage.** Every section maps to a task: the content split and
`instructions` field to Tasks 1-2; delivery, the harness table and the
cursor gap to Tasks 3-5; both call sites to Task 6; session context and the
flags to Task 7; the wizard section to Task 8; dogfooding and the status
line to Task 9. The spec's two inviolable rules are in Global Constraints.

**Placeholder scan.** Two steps deliberately describe rather than show,
because the exact code depends on a file the plan cannot quote without
reading it first: Task 6 Step 5 (`seedSession`'s config resolution) and
Task 8 Step 4 (`cmd/prompt.go`'s field builder). Both name the file, the
pattern to copy, and the grep that finds it. Task 5's test has a
deliberate typo flagged inline.

Task 8 was rewritten during this review. Its first draft asserted in-loop
validation with a test body left as a comment, and the design behind it
did not work: the scripted prompter returns one answer per field and
cannot model re-asking, so a validating loop in `runInitFlow` would spin.
The check is now a validator on the `Question`, wired into huh's hook,
which is both testable without a form and honest about whose job
re-asking is.

**Type consistency.** `Render(files []string) (string, error)`,
`Splice(existing, block string) string`, `GlobalPaths(agents []string)
([]string, []string)`, `Write(rw RemoteFiles, agents, files []string)
([]string, error)`, `Remote(run, write) RemoteFiles`,
`InstructionFiles(ctx, projectPath) ([]string, error)`, `TaskText(task,
issue string) (string, error)`, `WriteTask(client, user, session, text)
error` — each defined once and used with the same signature downstream.

**Known unknowns the executor must check, not guess.** The `[]string`
field-kind constant's spelling (Task 1 Step 5); whether `fieldValue` uses a
`derefStringSlice` helper (Task 1 Step 6); whether `reconcile.Client.WriteFile`
expands `$HOME` (Task 6 Step 3); whether `SessionDir` exists (Task 7 Step
3); `presetShape`'s shape (Task 8 Step 1). Each step says what to look at.
