# cloudlab

Ephemeral, declarative dev instances in the cloud — named instances, started
from a template, `shell`/`ssh` in when you need them, torn down when you
don't. The backend is a cloud VM, not a process running on your Mac, so
Docker Desktop or minikube isn't what's burning your battery. DigitalOcean
is the first (and for now, only) supported provider; the provider boundary
is designed to add others without touching the rest of the tool — see
[ADR-0008](docs/adr/0008-provider-abstraction.md).

> **Status:** design locked, implementation not started. See
> [`docs/architecture.md`](docs/architecture.md) and [`docs/adr/`](docs/adr/)
> for the full design.

## What it does

```bash
cd myproject                  # any git repo
cloudlab up                   # boots a VM (template = python by default), seeds the
                               # repo, reconciles the Nix environment, starts
                               # continuous two-way watch — one command, repo is live
cloudlab shell                # local subshell with envs pointing at the instance
                               # (e.g. DOCKER_HOST=ssh://... for docker template)
cloudlab sync ./dataset        # one-shot push of anything outside the repo
cloudlab download ~/results    # one-shot pull back
cloudlab down                  # tears the instance down
```

Every instance is named after the git repo it belongs to (derived from the
`origin` remote, so two different clones of the same repo share an instance)
and provisioned via Nix + home-manager rather than hand-rolled cloud-init
package lists — so the same declarative config that keeps your Mac's
environment reproducible also keeps your ephemeral cloud boxes reproducible.
[Claude Code](https://github.com/anthropics/claude-code) and
[aide](https://github.com/jskswamy/aide) are installed by default on every
instance, with credentials handled through aide's `sops`/`age` secrets
system rather than syncing raw auth material to a box that gets destroyed
and recreated.

## Templates

| Template | What it gives you |
|----------|--------------------|
| `python` | uv, Python toolchain, Jupyter (`connect` tunnels a remote Jupyter server to localhost) |
| `docker` | Docker + minikube — a general container/k8s dev box, no required project manifest |

Both templates share a common home-manager module (git, age, aide, Claude
Code) so template-specific YAML only needs to declare what's actually
different about that template.

## Extending the environment

Drop a `cloudlab.yaml` in your repo root to add packages beyond the
template's defaults:

```yaml
packages:
  - nodejs_22
  - postgresql_16

flakes:
  - url: github:someorg/custom-tool
    packages: [cli]
```

See [`docs/architecture.md`](docs/architecture.md#package-and-flake-composition)
for how this composes with the template's own modules, and why it's designed
to support full home-manager module composition later even though v1 only
resolves package lists.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — components, data flow, instance lifecycle
- [`docs/adr/`](docs/adr/) — why things are shaped the way they are, one decision per file

## License

MIT
