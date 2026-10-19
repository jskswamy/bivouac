## Unreleased

### Added

- `allowUnfreePackages` permits named unfree packages, such as the
  `terraform` an imported Home Manager module may install, which
  `bivouac provision` otherwise refuses to evaluate. Names are matched by
  `nixpkgs.lib.getName` and merged with the selected agents into the same
  narrow predicate; nothing else unfree is allowed. See `docs/config.md`.
- Custom cloud images. `bivouac image build --tag my-dev` builds a
  DigitalOcean snapshot in your own account with Nix, your login shell and
  your configuration's Nix store closure already in place, then sets
  `image = "tag:my-dev"` in the config it built from, so `up` skips the
  slow part of first boot. Machine identity, access and Home Manager
  activation still happen at every launch. Packer does the build; bivouac
  never installs it.
- `bivouac image generate` writes the same build as a standalone Packer
  bundle you can review, commit and run with plain Packer and a
  `DIGITALOCEAN_TOKEN`, without bivouac. It needs only `pkl`.
- `image` accepts a numeric image ID or `tag:<name>` besides a stock slug.
  A tag picks the newest ready image built for the instance's region and
  architecture. A missing or incompatible image, a size whose disk is too
  small, or a `shell` different from the image's fails before any Droplet
  is created.
- `bivouac image list`, `image tag` and `image prune` show, promote and
  delete your images. Builds keep the newest image plus one previous per
  tag, region and architecture by default (`--keep-previous`,
  `--no-prune`); only images bivouac built are ever pruned. See
  `docs/images.md`, including what a build trusts.
- `DIGITALOCEAN_API_URL` points bivouac at a DigitalOcean-compatible API
  other than DigitalOcean's, as it already does the Packer DigitalOcean
  plugin. bivouac says on stderr which API it is using whenever it is set.

### Changed

- `arch`, `image`, `tailscale` and `beads` set in your personal base config
  now apply when a project omits them. Previously the project's schema
  default silently replaced them. A project that sets one still wins, even
  with the default value.

### Fixed

- `session merge` works after you fold, rebase or re-sign the branch a
  session started from, which is routine when you merge several sessions
  in turn. It replays the commits since the session's recorded start
  instead of refusing, so a rewritten branch no longer leaves the session
  paused with nothing to do but `bivouac down`. It still refuses if that
  start commit has been garbage collected.
- `up` no longer refuses a symlinked `~/.ssh/config`. A symlink to a writable
  file is followed and edited in place, leaving the link intact. If the file is
  read-only (for example Home Manager's), `up` leaves it alone when it already
  has `Include <managed config>`, and otherwise prints the line to add.
- The built-in templates install `moshi-hook` 0.4.31. Its CDN no longer
  serves 0.3.26, so every provision from the previous pin failed while
  building the Home Manager environment.
- `up` and `provision` survive a dropped connection. The Home Manager switch
  now runs under systemd on the instance, and bivouac reconnects to follow it,
  so switching networks mid-provision no longer ends it with `operation timed
  out`. Running `provision` again attaches to a switch still in progress. A
  config with a `git+ssh` flake keeps the old behaviour, because a forwarded
  agent cannot outlive its connection.
