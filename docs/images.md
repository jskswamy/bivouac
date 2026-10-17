# Custom cloud images

A Bivouac image is a DigitalOcean snapshot that already has Nix, your login
shell and your configuration's Nix store closure in place. Launching from one
skips the slow part of first boot: `bivouac up` no longer installs Nix or the
shell, and most of the store paths provisioning needs are already there.

You build and own these images in your own DigitalOcean account. Bivouac
writes the Packer template and runs Packer for you; it does not host images
or schedule rebuilds.

```bash
bivouac image generate --tag my-dev        # write a Packer bundle, build nothing
bivouac image build --tag my-dev           # build it, point your config at it
bivouac up                                 # launches from image = "tag:my-dev"
```

## Trust boundary

Read this before you build, commit or share a bundle.

- **A build runs arbitrary Nix expressions.** The builder evaluates and
  builds everything your configuration names (template, flakes, modules,
  packages), with network access. Only build configurations you trust.
- **Cleanup cannot find secrets you embedded.** Before the snapshot is taken,
  the builder's temporary account, SSH keys, host keys, machine ID,
  cloud-init state, histories, logs and temporary files are removed. Anything
  a configuration deliberately puts into the Nix store, a token written into
  a setting say, is part of the closure and ends up in the snapshot.
- **The bundle is yours to review.** It holds the rendered Nix configuration
  and nonsecret metadata, and no credential. It is still a description of
  your environment: commit or share it with the same care as the
  configuration it came from.

No credential reaches the builder. The DigitalOcean token stays with Packer on
the machine running it. GitHub, DoltHub, Tailscale and agent credentials are
not loaded during a build at all, and nothing is enrolled or paired: those
happen at launch, per instance, exactly as they do on a stock image.

## Generate or build

| | `image generate` | `image build` |
|---|---|---|
| Writes a bundle | yes, to `./bivouac-image` by default | yes, to a new directory under `~/.cache/bivouac/images/` by default |
| Needs | the `pkl` CLI | `pkl`, `packer` on `PATH`, a DigitalOcean token |
| Makes cloud calls | no | yes: a temporary Droplet and a snapshot, both billed |
| Afterwards | nothing: run Packer yourself | checks the snapshot, sets `image = "tag:<tag>"`, prunes older images |

Both refuse an `--output` directory that is not empty, so a bundle is never
overwritten. Neither needs Nix or git on your machine: Nix runs inside the
builder.

### Which config is built

The first of these that exists is used. Presence decides, not validity: an
invalid project config is an error, not a reason to fall back to the base.

1. `--config <file>`, when given. A missing or invalid file fails.
2. The project's `bivouac.pkl`, found from the current directory or `--repo`
   as `up` finds it (see [Which `bivouac.pkl` gets read](config.md#which-bivouacpkl-gets-read)).
   It inherits from your base config as usual.
3. Your personal `base.pkl` on its own, loaded once.

`--config` and `--repo` both choose the config, so passing both is refused.
`--name` does not apply: an image belongs to no instance.

Building checks `region`, `size` and `template`; it does not need your
`sshKeys`, since Packer connects with a temporary key of its own.

### What a build supports

- **Architecture:** `x86_64` only. Every build starts from stock
  `ubuntu-24-04-x64`, whatever your config's `image` says, and never from an
  earlier image. `arch = "arm64"` fails rather than guess.
- **Inputs:** the builder is a remote machine, so a `template` or `flakes`
  URL that is a local path fails with a message naming it. Push it to a git
  host and reference that instead.
- **Private inputs:** a flake that needs authentication fails. Your SSH agent
  is never forwarded to the builder.

## Building with Bivouac

```bash
bivouac image build --tag my-dev
bivouac image build --config ./custom.pkl --tag my-dev
bivouac image build --tag my-dev --keep-previous 3
bivouac image build --tag my-dev --no-prune --json
```

Prerequisites:

- **Packer** on `PATH`. Bivouac never installs it. `packer init` (which
  `image build` runs) installs the DigitalOcean plugin, pinned to an exact
  version in the bundle.
- **A DigitalOcean token**, from `DIGITALOCEAN_TOKEN` or else
  `digitalocean_token` in your secrets file, the same sources `up` uses (see
  [The DigitalOcean token](config.md#the-digitalocean-token)). It is passed to
  the Packer process through its environment only: never into the bundle,
  the command line or the logs.

A build runs `packer init`, `packer validate` and `packer build`, reads the
new snapshot's ID from `packer-manifest.json`, then reads the snapshot back
from DigitalOcean and checks it is one `up` would launch: available, fully
marked, with the shell it was built for. Only then does it:

1. **Point your config at the tag.** It sets `image = "tag:<tag>"` in the
   config it built from, by replacing that one line or appending it, leaving
   the rest of the file alone. A config that is a symlink (as Home Manager's
   `base.pkl` is), read-only, in the Nix store, or whose `image` is an
   expression is never edited: the line to add is printed and the build
   still succeeds. Because the reference is a tag, later rebuilds leave the
   file unchanged.
2. **Prune older images** in the same scope, as described under
   [Retention](#retention).

The result names the image ID, the reference, the config path and the bundle
directory. The bundle is kept, on success and on failure, so you can inspect
what built any image; remove old ones from the cache when you no longer need
them.

### When a build fails

A failure names its phase (`generate`, `packer-init`, `packer-validate`,
`packer-build`, `manifest`, `verify`, `writeback` or `prune`), and with
`--json` the error object carries `phase`, `image_created`, `image_id` and
`cleanup`.

- A build that fails before or during Packer never prunes. Packer deletes
  its temporary Droplet itself; if it was interrupted, a `packer-*` Droplet
  or a partial snapshot may remain, and the error says to check for them.
- A snapshot that exists but fails verification is reported by ID, with
  `image_created` set; the config is not pointed at it and nothing is
  pruned. Inspect it with `bivouac image list` and delete it if unwanted.
- If the config cannot be written for any reason other than those above,
  the command exits nonzero and reports the new image; pruning still runs.
- If pruning fails after a good build, the command exits nonzero but reports
  the new image as built, lists which deletions completed and which are
  pending, and leaves the new and retained images in place. Run
  `bivouac image prune --tag <tag>` to retry.

## Using the bundle without Bivouac

A bundle is self-contained: Packer HCL, a README, the remote scripts, the
rendered Nix configuration and `metadata.json`, all referenced by paths
relative to the bundle. Edit it, commit it, or run it from CI with nothing
but Packer:

```bash
cd bivouac-image
export DIGITALOCEAN_TOKEN=...      # validate needs it set too
packer init .
packer validate .
packer build .
```

Region, size and account name are Packer variables, so
`packer build -var region=ams3 .` builds elsewhere without editing the
template; the region tag follows the variable. The snapshot carries the same
tags a Bivouac build gives it, so `image = "tag:<tag>"` launches it, and
`image list`, `tag` and `prune` treat it like any other. What standalone
Packer does not do is the Bivouac part: it neither updates your config nor
prunes older images. Run `bivouac image prune` yourself when you want to.

## Launching from an image

Set `image` in your project `bivouac.pkl` or your `base.pkl` to one of:

```pkl
image = "tag:my-dev"        // the newest ready image with that tag
image = "123456789"         // one image, by its numeric ID
image = "ubuntu-24-04-x64"  // a stock DigitalOcean slug, as before
```

A `tag:` reference picks the newest eligible image, by creation time and then
by ID. Eligible means a ready (`available`) snapshot carrying the full set of
Bivouac marker tags, built for the instance's `arch`, built in and available
in its `region`, and carrying the tag. An image ID must pass the same checks.
`up` also checks that the instance's `size` has at least the snapshot's
minimum disk.

Every one of these checks runs before a Droplet is created. When none
passes, `up` fails naming what it wanted: it does not copy the image to
another region, fall back to stock Ubuntu, or start a build.

The login shell is baked into the image, so the config's `shell` must match
the one the image was built with. A mismatch fails before the Droplet is
created, naming both shells: set `shell` to the image's, or build an image
for the shell you want.

`bivouac status` shows the reference an instance was created with and the
image ID it resolved to.

## What is baked, and what happens at launch

Baked into the image:

- Nix, installed with the Determinate installer, as on a stock launch.
- The login shell. Unlike a stock launch, which falls back to bash when the
  shell fails to install, a build fails instead, so an image never carries a
  shell tag it does not have.
- Your configuration's Home Manager activation package, built for the same
  account name `up` will use and rooted against garbage collection. It is
  built, not activated.

Done at every launch, exactly as on a stock image:

- A fresh machine ID and SSH host keys, the instance account, its authorized
  keys, sudo and lingering, and only then disabling root login.
- Normal provisioning: the current config is resolved, the Nix environment
  is built and activated with Home Manager, and credentials are placed.
  Anything you have added or that has moved since the build (a new package, a
  newer commit of a floating flake input) is fetched or built then; Nix
  reuses whatever store paths the image already has.
- Activation-time work. Activation scripts that download things (plugin or
  skill installs, for example) still run at launch, because nothing was
  activated during the build.

So the time saved is Nix installation, shell setup and store population, not
all of first boot. A launch is not finished until provisioning completes,
as before.

## Retention

Retention keeps the newest image plus `--keep-previous` older ones, per
scope. A scope is one tag, one architecture and one build region. The
default is `--keep-previous 1`, two images in all; `0` keeps only the newest;
a negative number is refused.

Only ready Bivouac images in the scope count. An image still building, one
that failed, an image built for another region or architecture, and a
foreign image that merely carries your tag are neither kept nor deleted.
Droplets are never touched.

- After a successful `image build`, the build's own scope is pruned
  automatically, never deleting the new image. The policy is printed before
  Packer starts and applied without a second prompt. `--no-prune` keeps
  everything; it contradicts `--keep-previous`, so passing both is refused.
- `image prune` applies the same rule on demand.

There is no lock between hosts. If CI builds or prunes images, serialize the
jobs that share a tag, architecture and region.

## Listing, tagging and pruning

These work by tag or ID alone and read no project config, so they run from
anywhere. Each takes `--json`.

```bash
bivouac image list
bivouac image list --tag my-dev
bivouac image list --tag my-dev --region blr1 --arch x86_64 --json
```

`list` shows your private images newest first, with ID, tags, creation time,
the regions they are available in, and a status: `managed` (a ready Bivouac
image, launchable and prunable), `not ready` (a Bivouac image still building
or that failed) or `foreign` (anything else, even with your tag; never
launched or pruned).

```bash
bivouac image tag 123456789 stable
```

`tag` adds a tag to a ready Bivouac image, for example to promote a build so
that configs using `image = "tag:stable"` pick it up. The image keeps its
other tags. Only Bivouac images are accepted: tagging is how an image enters
a tag's retention, so an arbitrary snapshot is refused rather than adopted
into pruning.

```bash
bivouac image prune --tag my-dev --dry-run
bivouac image prune --tag my-dev --keep-previous 1
bivouac image prune --tag my-dev --region blr1 --arch x86_64 --yes --json
```

`prune` lists what it would delete and asks first. Outside a terminal pass
`--yes`, or `--dry-run` to list without deleting. Without `--region` and
`--arch`, every region and architecture the tag was built for is pruned
separately, so a `blr1` build never pushes out an `nyc3` one. Each image is
re-checked just before deletion, and the prune stops rather than delete one
that changed since it was listed. An image already gone counts as deleted;
any other failure exits nonzero and lists completed and pending deletions.

## Costs

Images live in, and are billed to, your DigitalOcean account:

- **Each build** runs a temporary Droplet of your config's `size` for as
  long as the build takes, then takes a snapshot.
- **Each snapshot** is billed for its storage every month it exists. With
  the default retention that is two snapshots per tag, region and
  architecture; `--keep-previous` and `image tag` can raise that, and
  `--no-prune` or a standalone Packer build lets it grow until you prune.

See DigitalOcean's pricing for current rates. `bivouac image list` shows
what you are keeping.
