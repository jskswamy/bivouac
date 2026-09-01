# Contributing to cloudlab

## Development environment

This repo's `flake.nix` provides everything needed to build and test
cloudlab: Go, the Pkl CLI (pinned to the version `pkl-go`'s codegen
requires), `gopls`, and pre-commit tooling (`gofmt`, `golangci-lint`,
`nixfmt-rfc-style`, `deadnix`, `trufflehog`, plus general hygiene
checks).

### One-time setup

```bash
nix develop
```

This drops you into a shell with everything on `PATH`, and installs the
git pre-commit hook automatically via
[git-hooks.nix](https://github.com/cachix/git-hooks.nix).

### direnv (optional, recommended)

If you use [direnv](https://direnv.net) — or
[direnv-instant](https://github.com/Mic92/direnv-instant), also provided
by this flake, for an async/instant shell prompt — copy the example
config so `cd`-ing into this repo loads the dev shell automatically:

```bash
cp .envrc.example .envrc
direnv allow
```

`.envrc` itself isn't committed — direnv lists loading secrets (API
keys, deploy credentials) as a primary use case for it, so the
convention is to never commit one, even when — as here — your own
content is just `use flake` with nothing sensitive in it. Copying from
`.envrc.example` is a one-time step per clone.

### Running checks manually

```bash
pre-commit run --all-files   # every hook, without committing anything
nix flake check              # the same checks, in a form CI can run
go test ./...                # just the Go test suite
```
