## v0.2.0

### Fixed

- Session pull, merge, and delete now use the session's recorded repository
  path rather than the instance name, including nested `owner/repo` checkouts.
  Older sessions without a recorded path retain the instance-name fallback.
- Session merge and delete now remove the whole session directory, including
  its `TASK.md`, for nested `owner/repo` checkouts. Previously they removed only
  the owner directory and left `TASK.md` behind on the instance. Directories
  left by earlier releases are not cleaned up and can be removed by hand.

### Changed

- Session merge and delete now use one resumable retirement workflow. Cleanup
  closes only ownership-proven Herdr resources, records each destructive step,
  preserves other sessions and shared machines, and resumes with the same
  command after partial failure. Unreachable resources remain pending rather
  than being treated as absent.
- **Breaking:** `sshKeys` now accepts public-key file paths only, not MD5/SHA256
  fingerprints or numeric account IDs. Relative paths retain their declaring
  config's directory; base/project lists merge additively. `bivouac init`
  repairs old entries in their original files. See [migration](docs/config.md#ssh-keys).
- `up` checks selected files, local signing capability, account matches and SSH
  settings before final confirmation. Approved registrations upload public
  material only and never rename/delete account keys. Tokens need key-read
  access and, when additions are needed, key-create access.

### Added

- Recorded machine identities and one persistent `bivouac-<name>` SSH alias
  shared across sessions, provisioning, SSH/tmux/pair/forwarding, rsync, Git
  remotes and Herdr targets. Ordinary Git fetch and separate-process Herdr
  0.9.4 saved-machine reconnection are verified with disposable SSH fixtures.
- Explicit guidance for missing/changed identities and conflicting inherited
  SSH/Git settings, without silently trying unrelated agent keys. Old state
  without identity metadata keeps legacy authentication behavior.
- Machine-profile cleanup only after confirmed destruction; failed selected
  destroys retain state for retry. Session cleanup leaves the shared alias.

Merge, delete, forced offline delete, resume after partial cleanup and name
reuse were exercised against a live cloud instance, with a nested
`owner/repo` checkout and a second session kept alive throughout. Not
exercised live: physical hardware keys, direct-client (named-server) Herdr
attachment, issue-only work with Beads, and instance destruction. Beads
remotes store the shared alias, but actual Dolt interoperability remains an
external verification gap. See the [execution record](docs/superpowers/plans/2026-10-14-selected-ssh-key-execution-notes.md)
for evidence and limitations.
