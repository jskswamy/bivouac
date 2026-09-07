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
