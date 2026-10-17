# Contributing to bivouac

## Development environment

This repo's `flake.nix` provides everything needed to build and test
bivouac: Go, the Pkl CLI (pinned to the version `pkl-go`'s codegen
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

The flake's Go lint and vulnerability hooks need network access to resolve
modules and fetch the vulnerability database. CI uses `sandbox = false`
for these checks. On a Linux installation with sandboxing enabled, match
that setting for this invocation only:

```bash
nix flake check --option sandbox false --print-build-logs
```

The daemon must trust the invoking user to accept this restricted option.
If it prints `ignoring the client-specified setting 'sandbox'`, an
administrator must run the check with that option (for example, using
`sudo` where authorised). There is no need to disable sandboxing globally.
This check is network-dependent, not a hermetic/offline build.

### Testing against DigitalOcean without DigitalOcean

`go test ./...` already runs every image and launch command through the
real DigitalOcean client against an in-memory fake of its API
(`internal/provider/digitalocean/dofake`); no token is needed.

The end-to-end test in `e2e/` goes further: it runs the real `bivouac`
binary and real Packer with its DigitalOcean plugin against that fake,
whose droplets are QEMU virtual machines on this host. It builds an
image, launches from it and checks the launch used the baked
environment. It is opt-in, takes several minutes, and needs Linux,
passwordless `sudo` (for a bridge and tap devices), KVM through the
`kvm` group, and about 25 GB of disk:

```bash
sudo usermod -aG kvm "$USER"   # once; sg picks it up without a new login
sg kvm -c 'NIXPKGS_ALLOW_UNFREE=1 nix shell --impure nixpkgs#qemu_kvm nixpkgs#cdrkit nixpkgs#packer \
  -c nix develop -c env BIVOUAC_E2E_QEMU=1 go test -v -timeout 3h ./e2e/'
```

It builds from this checkout's committed templates. Logs and, after a
failure, the VM disks are kept in `~/.cache/bivouac-e2e/last-run`. The
decisions behind it are in
`docs/superpowers/specs/2026-10-17-cloud-images-testing-design.md`.
