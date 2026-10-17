## Unreleased

### Added

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

### Changed

- `arch`, `image`, `tailscale` and `beads` set in your personal base config
  now apply when a project omits them. Previously the project's schema
  default silently replaced them. A project that sets one still wins, even
  with the default value.

### Fixed

- `up` no longer refuses a symlinked `~/.ssh/config`. A symlink to a writable
  file is followed and edited in place, leaving the link intact. If the file is
  read-only (for example Home Manager's), `up` leaves it alone when it already
  has `Include <managed config>`, and otherwise prints the line to add.
