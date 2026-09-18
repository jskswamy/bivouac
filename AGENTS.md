# Working on bivouac

## Before you write code

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) for the dev environment. Everything
below assumes you are inside `nix develop` (or direnv has loaded it).

Tests come first. Write the failing test, watch it fail, then make it pass.
This codebase's tests are the design record for things that cannot be
exercised against a real instance — see `internal/lifecycle/session_localgit_test.go`,
which exists precisely because `seedSession`'s push has no in-process harness.

```bash
go test ./...              # the Go suite -- redirect to a file, never pipe (see below)
pre-commit run --all-files # gofmt, golangci-lint, nixfmt, deadnix, trufflehog
nix flake check            # the same checks, as CI runs them
```

Piping `go test ./...` into `tail`, `tee` or `grep` hangs forever after the
tests finish: a leaked `crashpad_handler` (sentry-native, via pkl) inherits
the pipe and keeps its write end open, so the reader never sees EOF. Redirect
to a file (`go test ./... > /tmp/out.log 2>&1`) instead.

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

## If you are working inside a bivouac session

You are on an ephemeral cloud VM, in a checkout at
`~/sessions/<name>/<repo>` on branch `bivouac/<name>`.

- **Do not push anywhere.** You have no credentials, no GitHub access, and no
  network git remote. There is nothing to push to and nothing to configure.
- **Commits are what survive.** Your work returns to the user's machine via
  `bivouac session pull` and `bivouac session merge`, run from their end.
  Uncommitted changes are swept into a checkpoint commit, so commit
  deliberately rather than relying on that.
- **Anything untracked in this checkout gets committed** by that checkpoint
  (`git add -A`). Do not leave scratch files, downloaded archives, or
  databases lying around in the working tree.
- The VM is destroyed on `bivouac down`. Nothing outside the repository
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
