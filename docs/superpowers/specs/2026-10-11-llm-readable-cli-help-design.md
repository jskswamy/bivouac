# LLM-readable CLI help

## Summary

Bivouac's CLI help should teach both humans and coding agents how to use its
commands without requiring them to read the architecture docs or source code.
The current `--help` output is valid Cobra help, but it is terse: for example,
`bivouac herdr --help` says only that it opens an interactive Herdr session,
without explaining session resolution, inside-Herdr saved-machine behavior,
workspace layout, or safe alternatives.

The change should add three complementary layers:

1. richer human-oriented help using Cobra's existing `Long` and `Example`
   fields, starting with Herdr and its adjacent session commands;
2. explicit `--json` output on commands where structured results are useful,
   especially read-only status/list commands; and
3. a machine-readable CLI manifest that exposes bivouac operations as
   MCP-shaped tool metadata: tool name, title, description, JSON Schema input,
   optional output schema, examples, side effects, preconditions, and safety
   annotations.

This lets an LLM ask bivouac itself what commands exist, inspect command result
schemas, and choose safe commands without scraping prose help or hallucinating
arguments. Normal human output remains the default unless a command explicitly
requests JSON.

## Current UX

Running `bivouac --help` shows Cobra's default text help: command names,
short descriptions, global flags, and a pointer to subcommand help.

Running `bivouac herdr --help` currently produces only:

```text
Open an interactive herdr session on the instance (background session that survives disconnects)

Usage:
  bivouac herdr [name] [flags]

Flags:
  -h, --help   help for herdr

Global Flags:
      --name string   instance name override
      --repo string   path to the repo to resolve identity from (overrides cwd-based resolution)
```

That is enough for a human who already knows bivouac's model, but not enough
for an LLM operating from the CLI surface alone. Missing facts include:

- `[name]` is an instance name, not a session name.
- The bivouac session is resolved from cwd, from a single live session, or via
  an interactive picker.
- Outside Herdr, bivouac launches `herdr --remote ssh://user@ip --session
  <session>` when a session resolves, otherwise Herdr's default remote session.
- Inside Herdr, bivouac registers one saved machine named for the bivouac
  instance and creates/focuses a workspace named for the bivouac session.
- If no session can be resolved inside Herdr, the command refuses and tells the
  user to start a session or run outside Herdr.
- The command is interactive and may modify Herdr's local saved-machine catalog.
- Related commands are often safer first steps: `bivouac session list`,
  `bivouac session start <name>`, `bivouac ssh`, and `bivouac tmux`.

Other commands have similar issues. Short text answers "what command is this?"
but rarely answers "when should an agent use it, what can it change, and what
should it do if resolution is ambiguous?"

## Standard way to expose CLI UX to LLMs

There is no single universal standard for "LLM-readable CLI help" equivalent
to POSIX `--help` for humans. The most relevant standards and conventions are:

- **MCP tools:** MCP's `tools/list` model exposes callable operations with
  `name`, `title`, `description`, `inputSchema`, optional `outputSchema`, and
  annotations. This is the best target shape because it is explicitly designed
  for agents discovering and invoking tools.
- **Function-calling tools:** OpenAI and Anthropic tool/function calling use
  the same core idea: tool name, prose description, JSON Schema parameters, and
  structured results.
- **Human CLI help:** `--help`, `Long`, `Example`, man pages, and shell
  completion remain the native UX for people and for LLMs reading plain text.
- **Agent documentation files:** `AGENTS.md`, `llms.txt`, and project docs give
  broader operating guidance, but they are less precise than a command tree and
  drift more easily from actual flags.

For bivouac, the best fit is to expose a **machine-readable MCP-shaped command
manifest** generated from the actual Cobra command tree plus explicit command
metadata. This is not a full MCP server in the first pass. It keeps bivouac a
normal CLI while making the metadata suitable for a future `bivouac mcp serve`
implementation if one becomes worthwhile.

## Desired behavior

### Human help

Commands should continue to support ordinary `--help` output. For commands
where misuse is easy or side effects matter, help should include:

- what the positional arguments mean;
- how identity/session resolution works;
- whether the command is interactive;
- important side effects;
- safe related commands; and
- examples.

Herdr is the first target. Its help should explain both modes:

- **outside Herdr:** starts/attaches a remote Herdr session on the instance;
- **inside Herdr:** registers/focuses an instance saved machine and a
  session-named workspace.

### Explicit JSON output

Add `--json` to commands whose output agents are likely to consume. The first
pass should prioritize read-only commands and adjacent session-discovery
commands, such as:

```bash
bivouac status --json
bivouac list --json
bivouac session list --json
bivouac session current --json
```

Where useful, side-effectful commands may also support `--json`, but the flag
changes only the reporting format, not the action itself. For example,
`bivouac herdr --json` may report the resolved instance/session and known side
effects after launching or focusing Herdr; it must not make the command safer,
less interactive, or non-side-effectful by implication.

JSON outputs should be deterministic and shaped for parsing, not table text
wrapped in strings. Each output shape should be small, documented by tests, and
allowed to evolve while bivouac is pre-stable.

### Machine-readable manifest

Add a dedicated command:

```bash
bivouac manifest
```

It should default to the first supported manifest format. The first supported
format should be MCP-shaped tools metadata; an explicit spelling is useful for
future extension:

```bash
bivouac manifest --format mcp-tools
```

The manifest must be generated from the real command tree and command metadata,
not maintained as a separate hand-written document. It should include a top-level
manifest version plus tools shaped around MCP's `tools/list` model:

```json
{
  "schemaVersion": "1",
  "name": "bivouac",
  "summary": "Disposable remote workspaces for coding agents",
  "tools": [
    {
      "name": "bivouac_herdr",
      "title": "Attach Herdr to a bivouac instance/session",
      "description": "Launches or focuses Herdr for a bivouac session.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string",
            "description": "Optional bivouac instance name, not session name"
          }
        },
        "additionalProperties": false
      },
      "outputSchema": {
        "type": "object",
        "properties": {
          "instance": { "type": "string" },
          "session": { "type": "string" },
          "interactive": { "type": "boolean" }
        },
        "additionalProperties": true
      },
      "examples": ["bivouac herdr", "bivouac --name myrepo herdr"],
      "metadata": {
        "commandPath": ["herdr"],
        "use": "herdr [name]",
        "interactive": true,
        "network": true,
        "destructive": false,
        "requiresInstance": true,
        "requiresRepo": false,
        "mayPrompt": true,
        "jsonOutput": true,
        "sideEffects": [
          "may register a Herdr saved machine",
          "may create or focus a Herdr workspace"
        ],
        "related": ["session list", "session start", "ssh", "tmux"]
      }
    }
  ]
}
```

The exact field names should be stable and documented in tests. The manifest is
not intended to be a public API with backwards compatibility guarantees yet, but
it should be deterministic so agents and tests can rely on it.

## Design

### Command metadata

Extend the command construction layer with optional metadata for LLM/tool use.
This should not be stored in a parallel global map keyed by strings if it can
be avoided. Prefer attaching metadata close to command construction, either by:

- extending `lookupCommandSpec` for commands created from the shared table; and
- adding a helper for hand-built commands such as `init`, `up`, `list`,
  `preset`, `provision`, and `secrets`.

Suggested metadata type:

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
    OutputSchema     map[string]any
}

type argumentMetadata struct {
    Name        string
    Required    bool
    Description string
    Schema      map[string]any
}
```

Cobra supports annotations on commands, but string-only annotations are a poor
fit for structured metadata. A small package-local registry keyed by command
pointer, or a typed wrapper helper that both creates the command and records
metadata, is acceptable if tests prove every registered command appears in the
manifest.

### Manifest generation

Add an internal function that walks a `*cobra.Command` tree and produces a
serializable manifest. It should:

- include tools in deterministic order;
- derive tool names from command paths unless explicit metadata overrides them;
- skip Cobra's generated completion command only if the design intentionally
  excludes it, otherwise include it as a normal command/tool;
- include local and inherited persistent flags in the input schema;
- include `Use`, `Short`, `Long`, and `Example` text in metadata where useful;
- include argument metadata where provided;
- include side-effect and safety metadata where provided;
- include whether the command supports `--json`; and
- avoid executing command `RunE` functions or resolving instances.

The manifest command must be read-only and offline. It must not inspect state,
call providers, call git, or connect to Herdr.

### Agent-context detection

Some coding agents set environment variables in subprocesses they spawn. Known
markers include `CLAUDECODE=1` for Claude Code, `AI_AGENT=pi`, and
`PI_CODING_AGENT=true` for Pi. Bivouac may also define an explicit override such
as `BIVOUAC_AGENT=1`.

Agent detection should be best-effort only. It may be used for soft hints, such
as suggesting `bivouac manifest` or `--json` when a command fails or when help is
shown. It must not silently flip normal command output to JSON. Explicit flags
and explicit bivouac environment variables always win.

### Herdr help text

Update `herdr` command metadata and text first. Its `Long` text should include:

- `[name]` means instance name;
- how the bivouac session is selected;
- outside-Herdr behavior;
- inside-Herdr behavior;
- what to do when no session exists; and
- related commands.

Its examples should include:

```text
bivouac herdr
bivouac --name jskswamy-bivouac herdr
bivouac session list
bivouac session start auth
```

Do not overfit to Herdr only. The metadata mechanism should support all
commands, but the first implementation may fill rich metadata only for Herdr
and core adjacent commands, with sensible defaults for the rest.

## Testing strategy

Write tests before implementation.

Human help tests:

- `bivouac herdr --help` mentions that `[name]` is an instance name.
- It mentions outside-Herdr remote session behavior.
- It mentions inside-Herdr saved machine/workspace behavior.
- It includes at least one example.

Manifest tests:

- The manifest command runs without invoking command actions.
- The JSON is valid and deterministic.
- The top-level manifest has `schemaVersion`, `name`, `summary`, and `tools`.
- The Herdr tool has MCP-shaped fields: `name`, `title`, `description`,
  `inputSchema`, optional `outputSchema`, `examples`, and `metadata`.
- The Herdr tool metadata includes `interactive: true`, `network: true`,
  `requiresInstance: true`, `destructive: false`, and side effects for Herdr
  saved machines/workspaces.
- The Herdr input schema says `name` is an optional instance name.
- Persistent flags `--repo` and `--name` appear in the Herdr input schema.
- A representative nested command such as `session start` appears with its
  positional argument and flags.

JSON output tests:

- Read-only commands with `--json` print valid JSON and no table text.
- JSON output is deterministic for fixture state.
- `--json` does not change command behavior beyond output formatting.
- Unsupported `--json` use produces a clear error or is absent from help; it
  should not be silently ignored.

Regression tests:

- Existing text help still works.
- Existing command execution paths do not change.
- The manifest command does not require a git repository, state file, provider
  token, Herdr installation, or network.

## Non-goals

- Do not build a full MCP server in this change. The manifest should be shaped
  so a future server can reuse the same metadata, but there is no
  `bivouac mcp serve` in the first pass.
- Do not silently switch normal command output to JSON based on agent
  environment variables.
- Do not change command behavior or argument parsing except for explicit
  `--json` output support.
- Do not require agents to use JSON; human `--help` remains supported.
- Do not promise long-term manifest schema stability beyond deterministic output
  and tests for the current schema.
- Do not attempt to document every command exhaustively in the first pass.
