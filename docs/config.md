# Configuring cloudlab: `cloudlab.pkl`

cloudlab reads a declarative config file, `cloudlab.pkl`, written in
[Pkl](https://pkl-lang.org) — a typed, validated alternative to YAML.
This doc explains every field, how personal defaults are reused across
projects, and what happens when something's missing or wrong.

If you've never used Pkl before: it looks like a typed config file
(`key = value`), not a programming language. You don't need to learn
Pkl deeply to write a `cloudlab.pkl` — the two worked examples in
`docs/examples/` cover the common cases.

You'll need the `pkl` CLI installed to evaluate these files (or just use
this repo's `nix develop`, which provides it). The very first time `pkl`
evaluates anything using cloudlab's schema on a given machine, it needs
network access once to fetch and cache the `pkl.golang` package the
schema depends on; every evaluation after that is fully offline.

Note also that a `cloudlab.pkl` file is *executed*, not just parsed —
see "A note on trust" near the end of this doc.

## Fields

| Field | Type | Required? | Default | Meaning |
|---|---|---|---|---|
| `region` | `String?` | Yes, after merge | none | DigitalOcean region slug, e.g. `"nyc3"`. Maps directly to the `Region` field `Provider.Create` sends. |
| `size` | `String?` | Yes, after merge | none | DigitalOcean droplet size slug, e.g. `"s-1vcpu-1gb"`. Maps directly to `Provider.Create`'s `Size`. |
| `template` | `String?` | Yes, after merge | none | Provisioning template name. The template catalog itself (what each name actually installs) is a separate, later feature — for now this is just a name cloudlab passes through. |
| `arch` | `String` | No | `"x86_64"` | Instance CPU architecture: `"x86_64"` or `"arm64"`. Maps to the Nix system used for template/flake resolution. |
| `sshKeys` | `Listing<String>?` | No | none | SSH key IDs/fingerprints already registered with your provider. |
| `packages` | `Listing<String>` | No | empty | Nix packages to install on the instance. |
| `flakes` | `Listing<Flake>` (`{url, packages, modules}`) | No | empty | Nix flakes to install, each with its own package list and an optional `modules` flag to also pull that flake's `homeManagerModules.default`. |
| `basePath` | `String?` | No | none | Overrides where cloudlab looks for your personal base config (see below). |

"Required, after merge" means: `region`/`size`/`template` don't have to
be set in your project's `cloudlab.pkl` itself, as long as your
personal base config supplies them (or vice versa) — see below. If
neither file sets one, `Resolve` fails with an error naming exactly which
field is still missing.

## Personal base config and reuse across projects

Most of your `cloudlab.pkl` settings — your SSH key, your usual droplet
size, packages you always want — don't change per-project. Instead of
repeating them in every repo, put them once in a personal base config:

- Default location: `$XDG_CONFIG_HOME/cloudlab/base.pkl`, or
  `~/.config/cloudlab/base.pkl` if `XDG_CONFIG_HOME` isn't set.
- Not committed anywhere — it's yours, local to your machine.
- Written exactly like a project `cloudlab.pkl` — same schema, same
  rule that it never declares its own `amends` (see below).

When cloudlab loads a project's `cloudlab.pkl`, it also checks for your
base config. If one exists, the two are merged:

- **Scalars** (`region`, `size`, `template`): the project's value wins
  if it set one; otherwise the base's value is used.
- **Lists** (`sshKeys`, `packages`, `flakes`): additive — your base's
  entries first, then the project's. Nothing is dropped from either
  side.

If your base config doesn't exist yet, this isn't an error — your
project's `cloudlab.pkl` is used on its own, and any field it doesn't
set is simply missing (which fails validation if it's one of the three
required ones).

The merge is exactly one level deep: if your base config itself sets a
`basePath` field, that's ignored — there's no recursive/chained
resolution beyond the project-plus-one-base merge described above.

### Pointing at a different base file

Set `basePath` in your project's `cloudlab.pkl` to use something other
than the default location:

```pkl
basePath = "~/.config/cloudlab/work-base.pkl"  // a different personal file
basePath = "./team-base.pkl"                    // a file checked into this repo, next to cloudlab.pkl
```

A `~`-prefixed path expands to your home directory. An absolute path is
used as-is. Anything else is resolved relative to the project file's
own directory — not wherever you happen to run `cloudlab` from — so a
relative `basePath` always means "the file next to this one."

## Writing your `cloudlab.pkl`

Your `cloudlab.pkl` is just field values — no `amends` line, no path to
cloudlab's schema, nothing to reference at all:

```pkl
region = "nyc3"
size = "s-1vcpu-1gb"
template = "python"
```

cloudlab carries its own copy of the schema (embedded in the `cloudlab`
binary itself) and points your file at it automatically before
evaluating it — you never need to know where that schema lives, and
there's no version of it to keep in sync with, since it's always
exactly the one built into whichever `cloudlab` you're running.

If your file *does* start with its own `amends` line (for example,
copied from an older doc or another Pkl project), `Resolve` rejects it
with a clear error rather than silently ignoring or honoring it —
there's no supported case where a `cloudlab.pkl` should reference a
different schema than the one its own `cloudlab` binary carries.

See `docs/examples/minimal/cloudlab.pkl` for the simplest possible
file, and `docs/examples/with-base/` for the base-merge pattern (a
`base.pkl` and a `cloudlab.pkl` that merges with it).

## A note on trust

`cloudlab.pkl` and any base config it merges with are evaluated by the
`pkl` CLI, not just parsed as inert data — Pkl is a real language, and
evaluating a file can read other files, make HTTP requests, and read
environment variables as part of producing its result, the same way
running a build script can. Treat a `cloudlab.pkl` from a source you
don't trust the same way you'd treat an untrusted script, not the same
way you'd treat a YAML file. For future work that wires this into a
live `up` command that might run against arbitrary repos, configuring a
more restrictive Pkl evaluator (limiting what a `cloudlab.pkl` file is
allowed to read or fetch) is a natural hardening step.

## Errors you might see

- **"missing required field(s) after merging project and base config: size, template"** — `region`/`size`/`template` weren't set in either your project file or your base config. Set them in one or the other.
- **"pkl CLI not found on PATH"** — install Pkl, or run inside this repo's `nix develop` shell if you're working on cloudlab itself.
- **"must not declare its own `amends` — cloudlab manages the schema reference automatically; remove that line"** — your `cloudlab.pkl` or base config starts with its own `amends`. Delete that line; cloudlab points your file at its own embedded schema automatically.
- A Pkl evaluation error (malformed file, wrong type for a field) is passed through with the file path it came from — Pkl's own error message names the exact line and problem.
