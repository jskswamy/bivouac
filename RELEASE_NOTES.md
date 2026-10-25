## v0.5.0

### Added

- Google Cloud (`gcloud`) provider support: provision, manage, and connect to
  ephemeral development VMs on Google Compute Engine with dedicated `gcloud`
  configuration blocks, service account credentials, native IAP tunneling,
  and custom Packer image builds.
- `bivouac secrets set <key>` command to store encrypted credentials from
  arguments, files (`--file`), `$EDITOR` (`--editor`), standard input, or
  interactive prompts.
- `bivouac status` (and `status --json`) now shows the active connection route
  (`tailscale`, `iap tunnel`, or `public ip`) and indicates when a Tailscale
  overlay is unreachable from the local machine and falling back to the base
  transport.

### Fixed

- Tailscale-enabled instances now treat Tailscale as an opportunistic overlay on
  top of the instance's base transport (GCP IAP tunnel or public IP). When
  Tailscale is stopped locally or unreachable on the remote VM, `ssh`, `tmux`,
  `sync`, `download`, `pair`, `connect`, `herdr`, `session` git operations, and
  `ssh bivouac-<name>` automatically fall back to IAP or public IP without
  host-key mismatches.
