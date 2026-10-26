# Configuring bivouac: `bivouac.pkl`

bivouac reads a declarative config file, `bivouac.pkl`, written in
[Pkl](https://pkl-lang.org) — a typed, validated alternative to YAML.
This doc explains every field, how personal defaults are reused across
projects, and what happens when something's missing or wrong.

If you've never used Pkl before: it looks like a typed config file
(`key = value`), not a programming language. You don't need to learn
Pkl deeply to write a `bivouac.pkl` — the two worked examples in
`docs/examples/` cover the common cases.

You'll need the `pkl` CLI installed to evaluate these files (or just use
this repo's `nix develop`, which provides it). The very first time `pkl`
evaluates anything using bivouac's schema on a given machine, it needs
network access once to fetch and cache the `pkl.golang` package the
schema depends on; every evaluation after that is fully offline.

Note also that a `bivouac.pkl` file is *executed*, not just parsed —
see "A note on trust" near the end of this doc.

## Fields

| Field | Type | Required? | Default | Meaning |
|---|---|---|---|---|
| `provider` | `"digitalocean"\|"gcloud"` | No | `"digitalocean"` | Cloud infrastructure provider. See [Google Cloud Platform](gcloud.md) for GCP setup and authentication. |
| `gcloud` | `GCloudConfig?` | No | none | Google Cloud-specific configuration (`project`, `zone`, `machineType`, `externalIP`, `diskSizeGb`). See [Google Cloud Platform](gcloud.md). |
| `region` | `String?` | Yes, after merge | none | DigitalOcean region slug, e.g. `"nyc3"`. Maps directly to the `Region` field `Provider.Create` sends. |
| `size` | `String?` | Yes, after merge | none | DigitalOcean droplet size slug, e.g. `"s-1vcpu-1gb"`. Maps directly to `Provider.Create`'s `Size`. |
| `template` | `String?` | Yes, after merge | none | Provisioning template: `"python"` or `"docker"`. Anything else is treated as a complete flake reference (`"<url>#<output>"`) and used as-is — see [Templates](#templates) below. |
| `arch` | `String` | No | `"x86_64"` | Instance CPU architecture: `"x86_64"` or `"arm64"`. Maps to the Nix system used for template/flake resolution. |
| `image` | `String` | No | `"ubuntu-24-04-x64"` | The image a new instance boots from: a stock DigitalOcean slug, a numeric image ID, or `"tag:<name>"` for the newest image you built with that tag. Can live in your base config — see [below](#image). |
| `tailscale` | `Boolean` | No | `false` | Install `tailscaled` and auto-join the instance to your personal Tailscale network during `up`. Requires `tailscale_authkey` in your personal secrets file — see [below](#tailscale). |
| `beads` | `"session"\|"dolthub"\|"off"` | No | `"session"` | How the agent's issue database reaches the instance — see [below](#beads). |
| `shell` | `"fish"\|"zsh"\|"bash"` | No | `"fish"` | The instance user's login shell, set once by cloud-init at creation and never changed afterwards. Can live in your base config — see [below](#shell). |
| `sshKeys` | `Listing<String>?` | No | none | Public-key file paths. `bivouac init` selects them; `up` resolves account keys and registers missing public keys only after confirmation. See [SSH keys](#ssh-keys). |
| `packages` | `Listing<String>` | No | empty | Nix packages to install on the instance. |
| `agents` | `Listing<"agy"\|"antigravity"\|"claude"\|"codex"\|"copilot"\|"cursor"\|"opencode"\|"pi">` | No | empty | Coding agent harnesses to install. A curated list rather than plain `packages` entries — see below. |
| `allowUnfreePackages` | `Listing<String>` | No | empty | Unfree packages to permit by name, for ones that arrive through an imported module rather than `agents` — see [below](#allowunfreepackages). Merges additively like `packages`. |
| `instructions` | `Listing<String>` | No | empty | Markdown files delivered to every configured coding agent on the instance. Paths are relative to the declaring file; merges additively like `packages`. |
| `flakes` | `Listing<Flake>` (`{url, packages, modules}`) | No | empty | Nix flakes to install, each with its own package list and a `modules` listing naming dotted paths into that flake's `homeManagerModules` to pull (e.g. `"tools.git"` for one tool, `"git-tools"` for a top-level group). |
| `herdrTabs` | `Listing<HerdrTab>` (`{label, panes}`; each pane `{label, command?, cwd?}`) | No | empty | Tabs and panes `bivouac herdr` lays out in the session's workspace when run inside herdr. Merges additively like `packages`, base's tabs first. See [herdr tabs](#herdr-tabs). |
| `settings` | `Mapping<String, String\|Boolean\|Int\|Listing<String>>` | No | empty | Overrides layered onto the final merged home-manager configuration, keyed by a dot-separated path (e.g. `"programs.git.userEmail"`, `"tools.tig.enable"`) — not scoped to any one flake or module. Merges by key: the project's value replaces the base's for a matching key, unlike every other list field's pure concatenation. |
| `basePath` | `String?` | No | none | Overrides where bivouac looks for your personal base config (see below). |

"Required, after merge" means: `region`/`size`/`template` don't have to
be set in your project's `bivouac.pkl` itself, as long as your
personal base config supplies them (or vice versa) — see below. If
neither file sets one, `Resolve` fails with an error naming exactly which
field is still missing.

### `agents`

```pkl
agents {
  "claude"
  "codex"
}
```

| Name | Installs | Binary | Licence |
|---|---|---|---|
| `antigravity` / `agy` | `antigravity-cli` | `agy` | **unfree** |
| `claude` | `claude-code` | `claude` | **unfree** |
| `codex` | `codex` | `codex` | Apache-2.0 |
| `copilot` | `github-copilot-cli` | `copilot` | **unfree** |
| `cursor` | `cursor-cli` | `cursor-agent` | **unfree** |
| `opencode` | `opencode` | `opencode` | MIT |
| `pi` | `pi-coding-agent` | `pi` | MIT |

Note the binary is not always the name: `antigravity` (or `agy`)
installs `antigravity-cli` and gives you `agy`, `cursor` installs
`cursor-cli` and gives you `cursor-agent`, `copilot` installs
`github-copilot-cli` (not nixpkgs' `copilot-cli`, which is AWS Copilot
and has since been removed from nixpkgs entirely).

These are a curated list rather than plain `packages` entries for two
reasons. The nixpkgs attribute isn't guessable from the tool's name —
`pi` ships as `pi-coding-agent`, `claude` as `claude-code` — and
`claude-code` is unfree, which nixpkgs refuses to build unless
explicitly permitted. That permission has to be granted where nixpkgs is
instantiated; a home-manager module can't set it, so `packages` alone
could never install it.

Selecting an unfree agent permits **exactly** that package and nothing
else, via an `allowUnfreePredicate` naming just the unfree agents you
asked for. It does not switch unfree software on in general, so an
unfree package listed in `packages` is still refused.

Anything not on this list still goes in `packages` as usual.

### `allowUnfreePackages`

Names unfree packages nixpkgs may evaluate even though you did not list
them yourself, typically because an imported module installs them. For
example, `jskswamy/nix-modules`' `tools.nvim` brings in Terraform, whose
`bsl11` licence nixpkgs otherwise refuses:

```pkl
allowUnfreePackages {
  "terraform"
}
```

Names are matched by `nixpkgs.lib.getName`, so they are package names
(`terraform`), not flake input names or attribute paths. They are
merged with the unfree agents you selected into the same narrow
`allowUnfreePredicate`: duplicates collapse, anything unlisted is still
refused, and unfree software is never switched on wholesale. Base and
project lists append, base first. A name outside letters, digits and
`_+.-` is rejected before anything is rendered.

### `tailscale`

Setting `tailscale = true` does two things: it installs and enables the
`tailscaled` daemon on the instance, and it makes `up` join the tailnet
automatically once the instance is reachable. You can also join an
already-running instance by hand with `bivouac tailscale`.

It needs `tailscale_authkey` in your personal secrets file — create it with
`bivouac secrets init`, then `bivouac secrets edit`. bivouac decrypts the
key just-in-time, streams it to the instance over SSH stdin, and zeroes its
in-memory copy immediately; the key is never a command-line argument and
never lands in plaintext on disk on either machine.

When generating that auth key in the Tailscale admin console, mark it
**Ephemeral**. Ephemeral keys make their devices self-remove from your
tailnet once they disconnect, so a destroyed instance (bivouac's VMs are
destroy-and-recreate, not long-lived) doesn't linger as a dead device
counting against your plan's device limit.

Instances on the tailnet also get a practical benefit beyond privacy:
`session start` points a session's git remote at the tailnet address when
one is available, so git traffic never crosses the public internet and the
remote survives a reboot that reassigns the public IP.

### `shell`

`"fish"` (default) | `"zsh"` | `"bash"`

The login shell of the instance user, so `ssh`, tmux panes and herdr panes
all start it. It is installed with apt (fish from its release-4 PPA, so it
is fish 4.x like the one in your Nix profile rather than Ubuntu's 3.7) and
chosen by the cloud-init script
that runs once at first boot, which is the only thing that ever sets it.
That is why it cannot be changed later: editing `shell` for an existing
instance makes `provision` fail, before it connects, with a message saying
so, rather than claim a change that never happens. An instance created
before this field existed is not held to it. Destroy and recreate the instance to
change it.

Set it once in your personal base config to use it everywhere instead of
repeating it in each project; a project's own value wins. With neither, the
default is `fish`.
Because a base value applies to every instance, changing it later makes
`provision` refuse on each existing instance that was created with a
different shell.

If the PPA cannot be added, Ubuntu's own fish is used. If the shell fails to
install or to run, the instance stays on bash and remains reachable.

An instance launched from an image you built gets the shell baked into
that image instead, so `shell` must match it; see [`image`](#image).

### `image`

One of three forms:

- **A stock DigitalOcean slug**, such as the default `"ubuntu-24-04-x64"`.
  It is passed to DigitalOcean unchanged, and the instance installs Nix
  and its shell at first boot.
- **A positive numeric image ID**, such as `"123456789"`: one image you
  built with `bivouac image build` or its Packer bundle.
- **`"tag:<name>"`**, such as `"tag:my-dev"`: the newest eligible image
  carrying that tag, by creation time and then by ID. This is what
  `bivouac image build --tag my-dev` writes for you, and because it names
  the tag rather than an image, rebuilding never changes the file.

An ID or a tag must name a ready Bivouac image: available, carrying the
full set of Bivouac marker tags, built for this config's `arch`, and
built in and available in its `region`. Its minimum disk must also fit
`size`. All of this is checked before any Droplet is created; when it
fails, `up` names what it wanted and stops. It never copies an image to
another region, falls back to the stock image, or starts a build.

The login shell is baked into an image, so a launch from an ID or a tag
also requires `shell` to match the shell the image was built with. A
mismatch fails before the Droplet is created, naming both shells: change
`shell`, or build an image for the one you want.

`image` in your base config applies to every project that does not set
it, so `image = "tag:my-dev"` there launches all your instances from your
image. A project that sets `image` itself, even to the stock default,
keeps its own value.

Building, listing, tagging and pruning images are covered in
[Custom cloud images](images.md).

### `beads`

`"session"` (default) | `"dolthub"` | `"off"`

How the agent's issue database reaches the instance.

`"session"` carries it over the session's own git remote as `refs/dolt/data` — the same
repository, over the same SSH channel, that the session's code already uses. No
credential reaches the VM.

`"dolthub"` additionally ships your DoltHub credential so the instance can sync against
the external remote your `.beads/` already names. The credential is account-wide and
lives in tmpfs on the VM for its lifetime; the session remote is still wired, so issues
come home over SSH even when DoltHub is unreachable. Requires `dolthub_creds` and
`dolthub_creds_id` in `bivouac secrets`.

`"dolthub"` mode reads two keys from `bivouac secrets`:

```yaml
dolthub_creds: |            # contents of ~/.dolt/creds/<id>.jwk
  {"kty":"OKP","crv":"Ed25519",…}
dolthub_creds_id: us8isf…   # the jwk's filename stem, written to user.creds
```

The id is stored beside the key rather than derived from your local
`~/.dolt/config_global.json`, so the instance's configuration does not depend on hidden
local state. If either is missing, bivouac warns and falls back to session mode.

A reboot clears tmpfs and leaves `~/.dolt` dangling; external sync then fails and session
mode is unaffected. `bivouac provision` restores it.

`bd dolt push` creates a real `refs/heads/__dolt_remote_info__` branch in the session
repository alongside `refs/dolt/data`. It is harmless but visible in `git branch`.

`"off"` wires nothing.

The setting is inert in a repository with no `.beads/`.

### GitHub

There is no `bivouac.pkl` field for this one. `gh` is installed on every
instance and works unauthenticated out of the box — fine for public repos,
subject to GitHub's anonymous rate limit of 60 requests an hour, which an
agent making a few lookups a session can reach.

To raise that ceiling, add `github_token` to `bivouac secrets`:

```yaml
github_token: ghp_…         # a PAT; public_repo scope is enough to read
```

Presence of the key is the only switch. If it is there, `up`/`provision`
decrypt it just-in-time and configure `gh` with it, the same way
`tailscale_authkey` and the DoltHub credential reach an instance: streamed
over SSH into tmpfs, never written to the instance's persistent disk, never
an environment variable. If it is absent, `gh` is simply left
unauthenticated — not a warning, not a failure.

The token is checked against GitHub before it is placed, because `gh` needs
to be told which account it belongs to. If GitHub rejects it — expired,
revoked, mistyped — bivouac says so and leaves the instance untouched
rather than installing a credential that would only fail there.

Nothing is overwritten: if `~/.config/gh` on the instance is already
something other than bivouac's own symlink — a real `gh auth login`, say —
bivouac warns and leaves it alone.

A reboot clears tmpfs, as with the other secrets placed this way.
`bivouac provision` restores it.

### Templates

`"python"` gives you `python312` and `uv`; `"docker"` gives you `docker` and
`minikube`, with the daemon and group membership already wired up. Both
build on a shared module every instance gets — `git`, `gh`, `age`,
`devbox`, `herdr`, `mosh`, `moshi-hook`, `tailscale`, `tmux`
(preconfigured), `glow`, plus `fish` and `starship`.

Any other value is passed straight through to home-manager as a flake
reference, so you can point `template` at your own flake's
`homeConfigurations` output. Note that `arch` is ignored in that case: the
`-<system>` suffix is only appended for the two built-in names.

## herdr tabs

`herdrTabs` tells `bivouac herdr` how to lay out a session's workspace
when it attaches from inside herdr. Nothing is declared by default —
what a useful tab looks like depends on the project's own workflow.

Sample project `bivouac.pkl`:

```pkl
herdrTabs {
  new HerdrTab {
    label = "editor"
    panes {
      new HerdrPane {
        label = "code"
        command = "nvim ."
      }
    }
  }
  new HerdrTab {
    label = "run"
    panes {
      new HerdrPane {
        label = "server"
        command = "npm run dev"
      }
      new HerdrPane {
        label = "logs"
        command = "tail -f server.log"
        cwd = "logs"
      }
    }
  }
}
```

Panes within a tab chain in declaration order: the first pane is the
tab's own pane, and each later one splits off the pane before it.

Tabs and panes are matched by label, so re-attaching a session adds
only whatever is still missing rather than duplicating what is already
there. If the merged config declares two tabs with the same label —
say both `base.pkl` and the project's `bivouac.pkl` declare a `logs`
tab — the first declaration wins and the later one is skipped; the
same rule applies to two panes with the same label inside one tab.

A pane's `command` runs only when that pane is first created. The pane
is labelled before its command is sent, so if starting the command
fails, a later attach finds the pane by its label and does not retry
it — bivouac reports a warning naming the pane instead of running the
command again.

`cwd` is relative to the session's checkout on the instance, unless it
is one of the two forms herdr resolves on the instance itself: an
absolute path, or one starting `~`. Either of those passes through
unchanged; anything else starts a pane in the checkout itself. herdr gives
every new workspace an empty tab `1`; the first configured tab takes it over
(renamed, its root pane becoming the tab's first pane), so nothing is left
in front of your tabs. A root pane starts in the checkout, so if the first
pane needs its own directory the tabs are created as usual and tab `1` is
closed once they exist. Either way this happens only in the attach that
created the workspace; a re-attach never renames or closes a tab you may be
working in. Laying
out tabs needs `bivouac herdr` to be run from inside herdr, and the
repository to have a `bivouac.pkl` — a repository without one gets no
layout, including any tabs a personal `base.pkl` declares.

Reaching the instance this way — for the workspace — needs herdr 0.9.1
or newer on *both* ends: this machine's herdr and the instance's. An
older herdr on either side, the instance's session server not running
(for example after a reboot — forwarding never starts one), or the
instance simply being unreachable will each fail with an error naming
the likely causes, including `bivouac provision` where that is the fix.

Tabs are the exception: they are laid out over one SSH connection
straight to the instance's herdr, not through `--machine`, which costs
about five seconds a call — too slow for a layout that makes a call per
tab and per extra pane. They need only the instance's herdr, and a tab
that cannot be laid out is a warning, not a failed attach.

## The DigitalOcean token

Three commands talk to the DigitalOcean API and need a token: `up`, `down`
and `status`. Everything else — `ssh`, `herdr`, `tmux`, `session start`,
`sync`, `connect`, `serve` and the rest — works from local state and SSH,
and needs no token at all.

bivouac looks in two places, in this order:

1. **`DIGITALOCEAN_TOKEN` in the environment**, if it is set and non-empty.
2. **`digitalocean_token` in your secrets file**, decrypted just-in-time.

Put the token in the secrets file with `bivouac secrets edit`:

```yaml
digitalocean_token: dop_v1_…
```

### Why the environment wins

The order is deliberate, and it is the opposite of the usual instinct that a
file should beat an environment variable.

Decryption goes through age, and commonly through an `age-plugin-yubikey`
identity whose touch policy can require physical presence. bivouac exists so
that agents can work unattended; an agent cannot touch a key. Reading the
secrets file first would make every `up` and `down` block on a key that is not
plugged in, which is precisely the situation this ordering avoids.

So the two sources are for two different callers. The secrets file is the
durable home for the token on a machine you sit at. `DIGITALOCEAN_TOKEN` is the
override an unattended agent, a CI job or a container sets, and it keeps that
path free of any hardware prompt.

Neither is unconditionally "the secure one". An exported environment variable
is weaker at rest and is inherited by every child process of that shell,
including any agent you launch from it; the encrypted file is stronger at rest
but useless where nobody can authorise a decryption. Pick per machine.

Because only `up` and `down` require the token, an interactive day costs at
most two decryptions — one when you bring an instance up and one when you take
it down. If your identity's touch policy makes even that unwelcome, note that
the policy is chosen when the age identity is generated
(`age-plugin-yubikey --touch-policy always|cached|never`), and that is the
right place to change it. bivouac deliberately does not cache the decrypted
token: doing so would quietly undo a policy you set in hardware.

`digitalocean_token` differs from every other key in the secrets file in one
way worth knowing: it authenticates bivouac's own API calls from your machine
and is **never** sent to an instance. `tailscale_authkey` and the DoltHub
credentials are streamed to the VM; this one never leaves your laptop.

### When neither source has it

`up` and `down` fail, naming both places that were tried — neither can create
or destroy a droplet without the API.

`status` degrades instead. Every field it prints except the live status comes
from local state, so it reports what it knows and marks the one unavailable
field:

```
$ bivouac status myinstance
Name:     myinstance
...
IP:       203.0.113.7
Status:   unknown (no live check: no DigitalOcean token: DIGITALOCEAN_TOKEN is unset, and …)
```

This is the same way `status` already renders an instance it cannot reach.

## Which `bivouac.pkl` gets read

Always the one in the repository's **main checkout** — the directory
holding the real `.git`, not a linked worktree.

An instance is identified by its repository, so every worktree of a repo
shares one instance, and one instance has one config. A `bivouac.pkl`
sitting beside you in a worktree is therefore *not* read when you run
`bivouac up` from there; the main checkout's is. This is deliberate —
two worktrees able to disagree about the same droplet would be worse —
but it is not what the file next to you suggests.

`up` prints the path it resolved in its confirmation prompt for exactly
this reason:

```
This will create instance "myrepo":
  Config:   /Users/you/src/myrepo/bivouac.pkl
  Region:   nyc3
  ...
Proceed? [y/N]:
```

If that path is not the file you have been editing, that is why. Edit
the one it names, or run `bivouac up` from the main checkout.

## Personal base config and reuse across projects

Most of your `bivouac.pkl` settings — your SSH key, your usual droplet
size, packages you always want — don't change per-project. Instead of
repeating them in every repo, put them once in a personal base config:

- Default location: `$XDG_CONFIG_HOME/bivouac/base.pkl`, or
  `~/.config/bivouac/base.pkl` if `XDG_CONFIG_HOME` isn't set.
- Not committed anywhere — it's yours, local to your machine.
- Written exactly like a project `bivouac.pkl` — same schema, same
  rule that it never declares its own `amends` (see below).

When bivouac loads a project's `bivouac.pkl`, it also checks for your
base config. If one exists, the two are merged:

- **Scalars** (`region`, `size`, `template`, `shell`): the project's value
  wins if it set one; otherwise the base's value is used. `shell` falls
  back to `fish` when neither sets it.
- **`arch`, `image`, `tailscale`, and `beads`**: the same rule, decided by
  whether the project file declares the field at all. A project that
  omits one gets the base's value, and the schema default only when
  neither file sets it. A project that sets one wins even when its value
  equals the default, so `image = "ubuntu-24-04-x64"` in a project
  overrides `image = "tag:my-dev"` in your base.
- **Lists** (`sshKeys`, `packages`, `agents`, `allowUnfreePackages`, `flakes`, `instructions`,
  `herdrTabs`): additive — your base's entries first, then the
  project's. Nothing is dropped from either side of the merge itself.
  `herdrTabs` has one further rule of its own after merging: a later
  tab or pane whose label an earlier one already used is skipped
  rather than laid out twice — see [herdr tabs](#herdr-tabs).
- **`settings`**: merged by key, not additively. Your base's key/value
  pairs apply first; the project's then replace any matching key and add
  any new one — the project always wins a collision, the same way it
  wins for the scalars above.

If your base config doesn't exist yet, this isn't an error — your
project's `bivouac.pkl` is used on its own, and any field it doesn't
set is simply missing (which fails validation if it's one of the three
required ones).

The merge is exactly one level deep: if your base config itself sets a
`basePath` field, that's ignored — there's no recursive/chained
resolution beyond the project-plus-one-base merge described above.

### Pointing at a different base file

Set `basePath` in your project's `bivouac.pkl` to use something other
than the default location:

```pkl
basePath = "~/.config/bivouac/work-base.pkl"    // a different personal file
basePath = "./team-base.pkl"                    // a file checked into this repo, next to bivouac.pkl
```

A `~`-prefixed path expands to your home directory. An absolute path is
used as-is. Anything else is resolved relative to the project file's
own directory — not wherever you happen to run `bivouac` from — so a
relative `basePath` always means "the file next to this one."

## Answering questions instead: `bivouac init`

Writing the file by hand is one way to configure bivouac. The other is
to be asked:

```bash
bivouac init      # create or edit this project's config, interactively
```

`up` does the same thing on its own when a repository has no
`bivouac.pkl` yet — it asks the questions, writes the files, and then
brings the instance up, in one command. Both need a terminal: with
stdin redirected or in CI, they refuse rather than opening a form that
nothing will ever answer.

### Where the answers go

Answers are split between the two files by what they are, rather than by
asking you which file to put them in:

| Personal → `base.pkl` | This project → `bivouac.pkl` |
|---|---|
| `region`, `size`, `sshKeys` | `template`, `agents`, `packages`, `beads`, `tailscale` |

`bivouac.pkl` is committed. Putting your personal SSH public-key path or your own
preferred droplet size in it would hand both to everyone who clones the
repo, and they'd provision with your key rather than theirs.

The personal questions are asked once. Every run after that shows what
they're set to and offers to change them.

### SSH keys

`sshKeys` contains **public-key file paths**, not account identifiers:

```pkl
sshKeys {
  "~/.ssh/id_ed25519.pub"
  "/keys/custom signing key.pub"
}
```

Relative paths are relative to the config file declaring them, not the
current working directory. Only a leading `~/` is expanded; shell variables
and `~another-user` are not. Base and project lists merge additively, with
each entry retaining its original directory. Every file must contain exactly
one authorized-key public key (comments and blank lines are allowed).
Private, missing, empty, malformed and multiple-key files are rejected.

**Breaking migration:** numeric account IDs and MD5/SHA256 fingerprints are
no longer configuration inputs, even if the account already has that key.
Errors name the entry and its declaring file. Run `bivouac init` to replace
old values in their original base/project files, or edit them to paths.
An explicit path such as `./12345` is still a path, not a numeric ID.

#### Selecting and unlocking keys

`bivouac init` lists discovered public files and accepts custom paths. Agent
and disk discovery are deduplicated without losing a matching disk path.
Account-only keys cannot be selected: an account registration does not prove
you can log in. An agent-only key needs a public reference first—copy exactly
one selected public-key line from `ssh-add -L` into a `.pub` file, then select
that file. This exports no private material.

At least one selected key must have a usable local signer. Bivouac prefers a
matching agent key, otherwise checks the matching private file (the public
path minus `.pub`). Custom filenames and spaces are supported. An encrypted
private key must already be unlocked in ssh-agent; bivouac does not ask for
or save its passphrase. Hardware-backed agent keys may require a touch during
the displayed signing-capability check. Other selected keys may be additional
recipients without local signers; the final summary identifies them.

Standalone `init` can optionally register a missing public key with explicit
approval when `DIGITALOCEAN_TOKEN` is already in the environment. It never
decrypts a token just to run setup, and declining registration retains your
path selection. Setup invoked implicitly by `up` never uploads keys.

#### Confirmation and credential scopes

`up` validates public files, usable signers, account key matches and effective
SSH settings before its final confirmation. The summary names existing keys,
pending additions, extra recipients, the machine alias and managed include.
Declining makes no account/VM writes or persistent SSH-config changes.

After approval, missing public keys are registered as `bivouac-<file-stem>`
(with a safe normalised stem); already-registered names are preserved. Only
public material is uploaded. Registration is add-only and retry-safe: account
keys are never renamed, replaced or deleted, including on `down`. Approved
additions remain registered if VM creation later fails.

The token needs authoritative SSH-key **read** access, and **create** access
when additions are needed, in addition to the droplet permissions your
workflow uses. Failed reads are not treated as an empty account. If your
token cannot create keys, import the public file through the web console or
the quoted `doctl` command in the error, then retry with read access. Restrict
credentials to the permissions you actually need.

#### Recorded identities and persistent connections

New machines record their approved identity references in local state.
Changing config affects future machines, not the identity of an existing
machine. Missing/changed public or private files and unavailable recorded
agent keys produce repair guidance; bivouac does not switch to unrelated
agent/default keys. Keep the recorded files and agent key available. Old
state without identity metadata retains its previous connection behavior.

Each new machine owns one `bivouac-<name>` SSH alias, shared by all its
sessions. Bivouac writes only its entries under the XDG config root's
`bivouac/ssh/` directory and adds one marked Include to `~/.ssh/config`,
preserving user content and permissions. Git session remotes and Herdr saved
machines use this persistent alias, so later connections work after the
creating bivouac process exits. SSH, tmux, pairing, forwarding and rsync use
the same selection. Generic local Git/signing and unrelated remotes remain
unchanged. Beads remotes also store the alias; actual Dolt interoperability
has not yet been integration-verified.

The alias maps to the recorded real endpoint, with `IdentitiesOnly yes` and
real-address host-key identity. Tailnet discovery updates the preferred
endpoint while retaining public-IP fallback. Git probes real endpoints—not
alias DNS—and scopes fallback to that transfer without changing its saved
remote or disabling host-key checks. A changed server key is an error, not
a reason to remove known-host entries automatically.

Broad user `IdentityFile` settings can add unrelated keys even when
`IdentitiesOnly` is set. Bivouac checks effective `ssh -G` settings and
reports conflicts rather than rewriting your hosts. Scope those settings
away from managed aliases (for example `Host * !bivouac-*`). Conflicting
`GIT_SSH_COMMAND`, `GIT_SSH`, repository `core.sshCommand` and Git URL rewrites
must likewise be scoped away or unset for bivouac-owned Git transfers.

If profile installation was interrupted after creation, the VM remains
tracked: fix the reported local config conflict and retry `bivouac provision
<name>`. Symlinked/nonregular user configs are rejected with manual repair
guidance. If local cleanup fails after a confirmed destroy, remove only the
named alias and its entry in `bivouac/ssh/profiles.json`, preserving other
machines. Removing a session never removes the shared SSH alias. Failed
selected-machine destruction retains state and alias for retry; successful
`down` removes only that machine's profile and removes the marked Include
when no managed machines remain.

### Only what you chose

The written file holds the answers that say something. Accepting a
default — leaving `beads` on `"session"`, saying no to `tailscale`,
adding no packages — writes no line for it, because the schema supplies
that value anyway and a file restating it is longer without being more
precise. Choose `beads = "off"` or turn `tailscale` on and the line
appears.

A field the file already declares keeps its line whatever you answer,
default included. Deleting a line because the value in it happens to
match a default would be the same unasked-for restructuring `init`
refuses elsewhere.

### Files that predate the split

A `bivouac.pkl` written by hand usually holds personal and project
settings together. `init` notices, says what it found, and offers to lift
the personal fields into your `base.pkl`. Declining leaves the file
exactly as it is — restructuring a committed file as a side effect of
changing one package would be a diff your colleagues have to read for no
reason.

Either answer resolves to the same config: a project file's scalars
override the base's and its listings merge additively, so moving a field
between them changes nothing about the result.

### Presets

At the end of a run, `init` offers to save the project's shape under a
name. The next project with no config is offered those presets as a
starting point.

```bash
bivouac preset list           # name, what it sets, when it changed
bivouac preset show <name>    # print the pkl
bivouac preset edit <name>    # the same questions, on that preset
bivouac preset delete <name>  # confirms
```

A preset's settings are **copied into** the new `bivouac.pkl`, never
referenced from it. A committed file reading its settings out of a preset
only you have would be incomplete for everyone else — and silently so,
since a base config that isn't there is skipped rather than reported. The
cost is that editing a preset doesn't reach projects already made from
it, which is the right trade for a file other people clone. It's also why
`preset delete` needs no dependency check: nothing can be pointing at one.

A preset keeps the project's shape, plus `region`/`size` only where the
run overrode your personal defaults — the case where a toolchain genuinely
needs particular sizing, such as one that wants more headroom than your
usual box. It never keeps `sshKeys`.

## Writing your `bivouac.pkl`

Your `bivouac.pkl` is just field values — no `amends` line, no path to
bivouac's schema, nothing to reference at all:

```pkl
region = "nyc3"
size = "s-1vcpu-1gb"
template = "python"
```

bivouac carries its own copy of the schema (embedded in the `bivouac`
binary itself) and points your file at it automatically before
evaluating it — you never need to know where that schema lives, and
there's no version of it to keep in sync with, since it's always
exactly the one built into whichever `bivouac` you're running.

If your file *does* start with its own `amends` line (for example,
copied from an older doc or another Pkl project), `Resolve` rejects it
with a clear error rather than silently ignoring or honoring it —
there's no supported case where a `bivouac.pkl` should reference a
different schema than the one its own `bivouac` binary carries.

See `docs/examples/minimal/bivouac.pkl` for the simplest possible
file, and `docs/examples/with-base/` for the base-merge pattern (a
`base.pkl` and a `bivouac.pkl` that merges with it). Or run
`bivouac init` and let it write one.

## Packages are checked before anything is built

`up` and `provision` check the config against the instance's own Nix
before the home-manager switch starts: the template resolves, every
`packages` entry exists in nixpkgs, every agent's package exists, and
each `flakes` entry has the outputs it claims. A problem names the entry
you typed:

```
provisioning validation failed:
  package "ripgrepp": not in nixpkgs (attribute 'ripgrepp' missing)
```

Every problem is reported at once, so a config with three typos takes one
round of fixing.

The check runs **on the instance**, not on your machine. That is where
every build happens, so it is the only Nix whose answer is the one that
will count — the same nixpkgs, the same system, the same result. It also
means bivouac needs no Nix installed locally; `pkl` is the only tool it
requires that you might not already have.

The trade-off is that the instance has to exist first, so a typo on the
very first `up` is caught after the VM is created rather than before. It
is caught in seconds, though, instead of part-way through a build.

## A note on trust

`bivouac.pkl` and any base config it merges with are evaluated by the
`pkl` CLI, not just parsed as inert data — Pkl is a real language, and
evaluating a file can read other files, make HTTP requests, and read
environment variables as part of producing its result, the same way
running a build script can. Treat a `bivouac.pkl` from a source you
don't trust the same way you'd treat an untrusted script, not the same
way you'd treat a YAML file. For future work that wires this into a
live `up` command that might run against arbitrary repos, configuring a
more restrictive Pkl evaluator (limiting what a `bivouac.pkl` file is
allowed to read or fetch) is a natural hardening step.

## Errors you might see

- **"missing required field(s) after merging project and base config: size, template"** — `region`/`size`/`template` weren't set in either your project file or your base config. Set them in one or the other, or run `bivouac init`.
- **"this needs a terminal to ask its questions"** — `bivouac init`, or `up` with no `bivouac.pkl`, was run with stdin redirected or in CI. Run it from a terminal, or write the file by hand.
- **"pkl CLI not found on PATH"** — install Pkl, or run inside this repo's `nix develop` shell if you're working on bivouac itself.
- **"must not declare its own `amends` — bivouac manages the schema reference automatically; remove that line"** — your `bivouac.pkl` or base config starts with its own `amends`. Delete that line; bivouac points your file at its own embedded schema automatically.
- **"no ready Bivouac image tagged "my-dev" for arch x86_64 in region nyc3"** — `image = "tag:my-dev"`, but no available image with that tag and the full set of Bivouac markers was built in and is available in that region for that architecture. Build one there (`bivouac image build --tag my-dev` with that region), or check what exists with `bivouac image list --tag my-dev`. Nothing was created.
- **"image … was built with login shell fish but the config asks for zsh"** — the image's baked shell differs from `shell`. Set `shell` to the image's, or build an image for your shell. Nothing was created.
- A Pkl evaluation error (malformed file, wrong type for a field) is passed through with the file path it came from — Pkl's own error message names the exact line and problem.
