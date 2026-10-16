## Unreleased

### Fixed

- `up` no longer refuses a symlinked `~/.ssh/config`. A symlink to a writable
  file is followed and edited in place, leaving the link intact. If the file is
  read-only (for example Home Manager's), `up` leaves it alone when it already
  has `Include <managed config>`, and otherwise prints the line to add.
