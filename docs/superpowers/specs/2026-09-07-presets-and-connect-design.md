# Composable Presets and a Generic Connect

## Status

Accepted, not implemented.

Supersedes the approach in cloudlab-5uf (retire the templates into
`common.nix`) and changes cloudlab-4tc's conclusion (k8s becomes a preset
rather than a documented recipe). Resolves cloudlab-c07 by making `connect`
general instead of Jupyter-shaped.

## Context

`template` is a scalar. It holds one of `"python"`, `"docker"`, or a complete
flake reference, and that is the whole vocabulary for saying what an instance
is for. This repository's own `cloudlab.pkl` says `template = "docker"`, which
means the instance it describes cannot also be a python instance. Two things
that compose perfectly well as home-manager modules cannot be selected
together, because the field that selects them holds one value.

Both built-in templates are also thin. `python.nix` is `python312` and `uv`;
`docker.nix` is `docker`, `minikube`, a `dockerd` systemd user service and a
`usermod -aG docker` activation script. Reading that thinness as the defect,
cloudlab-5uf proposed retiring both into `common.nix` and accepting the
consequence it named plainly: every instance would then run `dockerd`.

The thinness was a symptom. The defect is the exclusivity, and the codebase
already contains the fix. `agents` is a `Listing` of curated names, justified
in `Config.pkl` on grounds that transfer exactly:

> A curated list rather than free-form `packages` entries, because the nixpkgs
> attribute is not guessable from the tool's name -- pi ships as
> `pi-coding-agent`, claude as `claude-code`.

`k8s` should mean `kind` and `kubectl`, not `minikube` -- a conclusion
cloudlab-4tc derived at length. `python` should mean a base interpreter *and*
`uv` together, which is a judgment about how two attributes relate rather than
a lookup of one. Neither is something a `packages` line can carry.

Finally, `connect` is a stub: `cmd/lookup.go` declares it with no `run` field
and describes it as "Open a Jupyter tunnel to the instance (python template
only)". It is the last thing that made any template feel special, and it does
not work.

## Decisions

**`presets` is a composable `Listing`, `template` is not.** A new
`presets: Listing<"python"|"docker"|"k8s">` selects any number of curated
modules. `template` keeps only its bring-your-own job: a complete flake
reference that replaces the whole module (cloudlab-c2b's decision, now
unambiguous rather than overloaded).

**`common.nix` does not grow.** It keeps exactly what every instance needs
regardless of purpose -- `git`, `age`, `devbox`, `herdr`, `mosh`,
`moshi-hook`, `tailscale`, `tmux`, `fish`, `starship`. Nothing moves into it.

**Docker becomes a preset, not the baseline.** It keeps its `dockerd` unit and
group activation, and an instance gets a Docker daemon only when it asks for
one. This is the point of composability: cloudlab-5uf had to force docker into
the baseline because a scalar `template` left it nowhere else to live, and
swallowed "every instance runs dockerd" as the price. A plural field removes
the need to pay it.

**A preset encodes knowledge, not preference.** It earns its place when the
right package is not guessable (`kind` over `minikube`), when several packages
travel together (`python312` + `uv`), or when a module is required (`dockerd`).
It does not exist to save typing. `tig` and `lazygit` are interactive TUIs a
human uses over SSH and an agent never opens; they need no module and no
curation, so they belong in the personal base config at
`~/.config/cloudlab/base.pkl`, whose list fields already merge additively into
every project.

**`connect` resolves an address before it builds a tunnel.** Instances on a
tailnet are already reachable, so `connect` prints or opens
`http://<tailnet-ip>:<port>` and exits, with no process and no state. It falls
back to a foreground `ssh -L` only when there is no tailnet. Given a bare
invocation it asks the instance what is listening and offers a choice.

**Jupyter stops being special.** With a generic `connect`, a notebook is a
package plus a systemd unit, expressible as a preset later or as a
`flakes { modules = true }` entry today. Nothing about it requires bespoke
support in cloudlab.

### Rejected alternatives

**Retire the templates into `common.nix` (cloudlab-5uf as written).** Rejected
because it forces `dockerd` onto every instance and discards the curation that
makes `python` mean `uv`. It was the right response to a scalar field, and the
wrong response once the field can hold a list.

**Keep `template` scalar and add more names.** `"k8s"`, `"rust"`, `"node"` as
additional values. Rejected: it leaves the actual defect in place. A python
project that also wants a cluster is still stuck.

**Named services declared in `cloudlab.pkl` for `connect`.** A `services` block
mapping names to ports reads nicely, but invents a schema and can only know
about services declared ahead of time -- never the one an agent just started.
Port plus discovery covers both and adds no schema.

**Always establish an SSH forward.** Uniform, but runs a process that must be
tracked and torn down. That is what `state.Record.TunnelPID` exists for, and
tracking it is what made the original design awkward enough to be abandoned
half-built.

## Architecture

```
common.nix              every instance, no exceptions
                        git age devbox herdr mosh moshi-hook
                        tailscale tmux fish starship
        +
presets { ... }         curated, composable, may carry modules
                        docker -> docker + dockerd unit + group activation
                        python -> uv
                        k8s    -> kind + kubectl (imports docker)
        +
packages { ... }        any nixpkgs attribute, ungated
        +
flakes { modules }      bring your own module
        or
template = "<ref>"      bring your own everything
```

Composition is the home-manager module system doing what it already does. The
rendered wrapper flake lists modules; adding more is adding list entries.

The two halves of this spec share a motivation but no code. `presets` touches
the Pkl schema, the templates flake and `Render`; `connect` touches `cmd` and
`internal/lifecycle`. They can be implemented and shipped in either order, and
the implementation plan should treat them as separate tracks rather than
sequencing one behind the other.

**Presets may depend on other presets, and depend transitively.** `kind` runs
Kubernetes nodes as Docker containers, so `k8s` without `docker` is not a
degraded k8s -- it is a broken one. That is a dependency, not a suggestion, and
it is expressed the way the module system already expresses dependencies:
`k8s.nix` imports `docker.nix`.

Selecting `k8s` therefore pulls docker in whole, including its `dockerd` unit
and group activation. The module system deduplicates by path, so naming both
(`presets { "docker" "k8s" }`) resolves to the same closure as naming either.
Docs state the implication; nothing needs to enforce it.

## Components

### `internal/config/Config.pkl`

```pkl
/// Curated module sets to compose onto the baseline. Unlike `packages`,
/// a preset may carry a systemd unit or activation script, and encodes
/// which packages belong together -- `k8s` installs `kind` and
/// `kubectl` rather than `minikube`, and pulls in `docker`, which
/// `kind` needs to run nodes at all.
presets: Listing<"python"|"docker"|"k8s"> = new Listing {}
```

`template` narrows to a bring-your-own flake reference and nothing else. The
built-in names `"python"` and `"docker"` are removed outright rather than
deprecated: the project has no released users, and carrying a compatibility
path for a value only its own repository sets would cost more than the rename.

`builtinTemplates` in `internal/provisioning/template.go` therefore disappears,
and with it `ResolveTemplateRef`'s expansion branch -- a `template` value is now
always a complete flake ref, used as-is.

### `templates/`

`flake.nix` exports `homeManagerModules = { common, docker, python, k8s }`.

`homeConfigurations` stay one per preset per system, and gain a `common`-only
entry -- but they are made to mean something, which today they do not.

Nothing builds them. CI runs `nix flake check` against the repo root
(`ci.yml`), whose `checks` output is only `pre-commit-check`; `templates/` is a
separate flake with its own lock file that CI never enters. `nix flake check`
would not build `homeConfigurations` even if pointed at it, since that is not
an output type it recognises. So `templates/` has no automated coverage at all
today: a mistyped package name in `docker.nix` surfaces when a user provisions,
not when the change lands.

Presets make that worse by multiplying the modules, so this spec closes it.
`templates/flake.nix` gains a `checks` output building each preset's
`activationPackage`, and CI gains one step:

```yaml
- run: nix flake check ./templates --print-build-logs
```

That is what makes an entry per preset worth having, and it is the coverage the
existing two templates should already have had.

The preset modules stop doing `imports = [ ./common.nix ]`. Common is included
once by the renderer, which removes a double-import that only worked because
the module system tolerated it.

| file | contents |
|---|---|
| `common.nix` | unchanged |
| `docker.nix` | `docker`, the `dockerd` unit, the group activation. **`minikube` removed** |
| `python.nix` | `python312` and `uv`, unchanged. `python312` is the system-level base interpreter, always present; `uv python install` supplies any other version a project pins |
| `k8s.nix` | new: `kind`, `kubectl`, `imports = [ ./docker.nix ]` |

Dropping `minikube` resolves the collision `docker.nix` documents today, where
`pkgs.kubectl` cannot be added because minikube ships its own `bin/kubectl` at
the same path. `kind` ships none, so `kubectl` is added explicitly and the
conflict disappears rather than being worked around. Anyone who wants minikube
writes `packages { "minikube" }`.

### `internal/provisioning`

`Render` takes a preset list and emits one module line per entry:

```nix
modules = [
  template.homeManagerModules."common"
  template.homeManagerModules."docker"      # per preset
  { cloudlab.tailscale = true; }
  ...
];
```

`ResolveTemplateRef`'s `-<system>` suffix, appended for `homeConfigurations`
naming and then stripped again by `templateModuleName` before every module
lookup, is not needed on the preset path: presets name modules directly. The
round-trip stays only for a bring-your-own `template` ref, whose output name
cloudlab does not control.

### `cmd` and `internal/lifecycle`

`runConnect` fills the declaration that already exists in `lookupCommandSpecs`,
and its `short` stops mentioning Jupyter. Port discovery is a small addition
beside the existing `TailscaleIP`. `internal/tui`'s picker is already used for
session selection and is reused for choosing a port.

`state.Record.TunnelPID` survives, finally used, but only on the fallback path.

## Data flow

### Reconcile with presets

```
cloudlab provision
  config.Resolve            base.pkl + cloudlab.pkl, lists merged additively
  presets -> module names   ["docker", "k8s"]
  Render                    common + docker + k8s + packages + agents + flakes
  ship flake, home-manager switch
```

### Migrating the one file that exists

`template = "docker"` stops being valid, so this repository's own
`cloudlab.pkl` changes in the same commit:

```pkl
template = "docker"      ->      presets { "docker" }
```

There is no compatibility shim and no deprecation window. Any other
`cloudlab.pkl` naming a built-in fails Pkl evaluation with a message naming
`presets`, which is the correct outcome while the project has no released
users.

A `template` value that is set at all is a flake reference and is used as-is,
unchanged from today.

### `connect`

```
cloudlab connect [port]
  resolve instance -> record.IP, record.User
  TailscaleIP(ip, user)
    "100.x"  -> print/open http://100.x:<port>, exit
    ""       -> ssh -L <port>:localhost:<port>, foreground until Ctrl-C
  no port    -> `ss -tlnp` over SSH -> picker -> as above
```

Nothing is written to state on the tailnet path, which is the common one.

## Error handling

A port nothing is listening on is reported as such rather than opened as a
dead URL -- `connect` already has the discovery query available to check.

Discovery falls back to asking for an explicit port when `ss` is missing,
rather than failing.

An unknown preset name fails at Pkl evaluation, since the `Listing` is typed --
the same way an unknown `agents` entry does today, and with no Go-side
validation needed.

A `template` naming a removed built-in fails at Pkl evaluation like any other
bad value, with a message pointing at `presets`. A `template` naming a flake
that does not exist fails where it already does, in `home-manager switch`.

## Testing

`render_test.go` gains cases for: an empty preset list, one preset, several
presets (order stable), a preset list alongside `packages` and `flakes`, and a
bring-your-own `template` with presets also set.

`template_test.go` loses its built-in-expansion cases along with
`builtinTemplates`, and keeps coverage that a bring-your-own ref passes through
untouched.

`connect` is table-driven over captured `ss -tlnp` output in `testdata/`, plus
the two branches of `TailscaleIP` returning an address or empty. The SSH
forward is exercised through the same fake SSH server `ssh_test.go` already
starts; no test needs a real instance.

A Pkl-level check that an unknown preset fails evaluation belongs with the
existing config tests.

## Costs

Two ways to install a package now exist -- `presets` and `packages` -- and the
line between them is a judgment call. The rule is written above (knowledge, not
preference) but it will be argued about, and a preset added for convenience
rather than curation would erode it.

`k8s` importing `docker` means selecting it starts a daemon the user did not
name. That is what a transitive dependency does, and `kind` cannot work
otherwise, so it is documented rather than avoided.

Removing the built-in `template` names is a breaking change taken deliberately,
on the grounds that the project has no released users. That justification
expires: once anyone else has a `cloudlab.pkl`, a change of this shape needs the
compatibility path this one skips.

Instances stop getting `docker` unless asked. That is the intended behaviour
change, but it means an existing instance reconciled after this lands loses its
Docker daemon unless its `cloudlab.pkl` names the preset.

## Out of scope

cloudlab-de5, cloudlab-du7 and cloudlab-qxy. That cluster needs an audit rather
than a design: `du7` records "Provisioning sub-project stays artifacts-only, no
CLI wiring" but `internal/provisioning` is imported by both `reconcile.go` and
`lifecycle.go`, and three of `de5`'s four open questions are answered by
shipped code. The live remnant is `provisioning.Validate`, which is written,
tested, and called from nowhere.

cloudlab-p05, in flight in a session, which adds `beads` to `common.nix`.
Whichever of the two lands second resolves a small conflict there.

A Jupyter preset. `connect` no longer requires one to exist, so it can be added
later on its own merits, by the same rule as any other preset.
