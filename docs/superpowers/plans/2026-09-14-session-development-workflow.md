# Session Development Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the workflow described in
`docs/superpowers/specs/2026-09-14-session-development-workflow-design.md`
discoverable by an agent working inside a cloudlab session, by referencing
it from `AGENTS.md`.

**Architecture:** One additive edit to `AGENTS.md`'s existing
"If you are working inside a cloudlab session" section: a short new
subsection naming the three entry points and the two hand-back steps,
linking to the spec for full detail. No other file changes -- the spec
itself is the contract; `AGENTS.md` only needs to point at it.

**Tech Stack:** Markdown only. No code, no tests to run beyond reading
the result back.

## Global Constraints

- Match `AGENTS.md`'s existing terse, bulleted style -- it is a short
  reference doc, not prose (see the "If you are working inside a
  cloudlab session" section already there for the tone to match).
- Commit messages: classic style, imperative subject ≤50 chars,
  capitalised, no trailing period, no `type:` prefix, body wrapped at
  72 columns. Never an AI co-author trailer.

---

### Task 1: Reference the workflow spec from AGENTS.md

**Files:**
- Modify: `AGENTS.md:37-52` (the "If you are working inside a cloudlab
  session" section)

**Interfaces:**
- Consumes: the spec at
  `docs/superpowers/specs/2026-09-14-session-development-workflow-design.md`
  (already committed as this session's first commit, `9c72fb6`) --
  read it to confirm the three entry points and two hand-back steps
  named below still match its content before writing the addition.
- Produces: nothing further downstream reads this section
  programmatically; it is documentation for a human or agent reading
  `AGENTS.md` directly.

- [ ] **Step 1: Re-read the spec to confirm current wording**

Run: `cat docs/superpowers/specs/2026-09-14-session-development-workflow-design.md`

Confirm it still describes exactly: three entry points (spec only; spec
and plan; beads story/epic only) and two hand-back steps (`refactor:scan`,
then `commit-tools:review-commits`). If the wording has drifted from what
this task assumes, update the paragraph below to match the spec, not the
other way around -- the spec is the contract.

- [ ] **Step 2: Add the new subsection to AGENTS.md**

Insert immediately after the existing bullet list in the
"If you are working inside a cloudlab session" section (i.e. after the
"The VM is destroyed on `cloudlab down`..." bullet, before end of file):

```markdown

### Development workflow

Enter at whichever stage already exists for the task: a spec with no
plan goes through `writing-plans` then `executing-plans`; a spec and
plan both written goes straight to `executing-plans`; a bare beads
story or epic goes through the full pipeline, `brainstorming` first.
Before handing back, run `refactor:scan` against what changed and fix
its findings, then `commit-tools:review-commits` to fold any TDD-churn
commits into logical units. Full detail:
[`docs/superpowers/specs/2026-09-14-session-development-workflow-design.md`](docs/superpowers/specs/2026-09-14-session-development-workflow-design.md).
```

The full, exact `AGENTS.md` content after this edit must be:

```markdown
# Working on cloudlab

## Before you write code

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) for the dev environment. Everything
below assumes you are inside `nix develop` (or direnv has loaded it).

Tests come first. Write the failing test, watch it fail, then make it pass.
This codebase's tests are the design record for things that cannot be
exercised against a real instance — see `internal/lifecycle/session_localgit_test.go`,
which exists precisely because `seedSession`'s push has no in-process harness.

```bash
go test ./...              # the Go suite
pre-commit run --all-files # gofmt, golangci-lint, nixfmt, deadnix, trufflehog
nix flake check            # the same checks, as CI runs them
```

## Conventions

Shell out to external tools (`sops`, `tailscale`, `nix`, `git`, `bd`) rather
than linking them as libraries. That is deliberate and consistent throughout.

Comments explain *why*, not *what*. Match the density of the file you are in;
`internal/lifecycle/session.go` is the reference for how much reasoning a
non-obvious decision deserves.

Commit messages use the classic style: an imperative subject of 50 characters
or fewer, capitalised, no trailing period, no `type:` prefix. Body wrapped at
72 columns, explaining why rather than how. **Never add an AI co-author
trailer** — no `Co-Authored-By` naming Claude, Anthropic, or any model.

Design specs live in `docs/superpowers/specs/`. A spec is the implementation
contract; if the code and the spec disagree, say so rather than silently
diverging.

## If you are working inside a cloudlab session

You are on an ephemeral cloud VM, in a checkout at
`~/sessions/<name>/<repo>` on branch `cloudlab/<name>`.

- **Do not push anywhere.** You have no credentials, no GitHub access, and no
  network git remote. There is nothing to push to and nothing to configure.
- **Commits are what survive.** Your work returns to the user's machine via
  `cloudlab session pull` and `cloudlab session merge`, run from their end.
  Uncommitted changes are swept into a checkpoint commit, so commit
  deliberately rather than relying on that.
- **Anything untracked in this checkout gets committed** by that checkpoint
  (`git add -A`). Do not leave scratch files, downloaded archives, or
  databases lying around in the working tree.
- The VM is destroyed on `cloudlab down`. Nothing outside the repository
  survives.

### Development workflow

Enter at whichever stage already exists for the task: a spec with no
plan goes through `writing-plans` then `executing-plans`; a spec and
plan both written goes straight to `executing-plans`; a bare beads
story or epic goes through the full pipeline, `brainstorming` first.
Before handing back, run `refactor:scan` against what changed and fix
its findings, then `commit-tools:review-commits` to fold any TDD-churn
commits into logical units. Full detail:
[`docs/superpowers/specs/2026-09-14-session-development-workflow-design.md`](docs/superpowers/specs/2026-09-14-session-development-workflow-design.md).
```

- [ ] **Step 3: Verify the referenced path resolves**

Run: `test -f docs/superpowers/specs/2026-09-14-session-development-workflow-design.md && echo OK`
Expected: `OK`

- [ ] **Step 4: Verify AGENTS.md still renders as valid Markdown and matches the block above exactly**

Run: `diff <(sed -n '1,63p' AGENTS.md) -` and paste the full expected
content from Step 2 on stdin, or simply `cat AGENTS.md` and eyeball it
against the block in Step 2 -- there is no markdown linter in this
repo's `pre-commit` config (`gofmt`, `golangci-lint`, `nixfmt`, `deadnix`,
`trufflehog` only), so a direct read is the check.

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md
git commit -m "$(cat <<'EOF'
Reference the session workflow spec from AGENTS.md

The workflow design doc named three entry points and two hand-back
steps but nothing pointed a session at it -- it sat in docs/ with
no reader. Add a short subsection to the existing "If you are
working inside a cloudlab session" section linking to it, matching
that section's own terse style.
EOF
)"
```

## Self-review notes

- **Spec coverage:** the spec's two H2 sections ("Starting a session",
  "Before handing back") are both represented in the new AGENTS.md
  paragraph; the "Out of scope" section names nothing this task needs
  to act on.
- **Placeholder scan:** none -- every step names the exact file, exact
  text, and exact commands.
- **Type consistency:** n/a, no code/functions introduced.
