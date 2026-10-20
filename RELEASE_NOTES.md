## Unreleased

### Added

- `bivouac session status <name>` shows, live from the instance, how far a
  session is through the epic it was started for: planned issues closed,
  work discovered along the way, and anything that needs attention.
- `bivouac session issues <name>` lists every issue a session created,
  updated or closed, and whether each one is on this machine yet.
- `session pull` and `session merge` end with a one-line receipt of the
  issues they brought back. Merge now checks that every issue the session
  touched has landed, and pauses with "issues not synced" when one has not;
  running merge again pulls and checks again. `--accept-unsynced` lets a
  merge proceed past issues that can never sync, and lists them.

### Fixed

- Session issue reports and merge verification no longer mark stale local
  issues as synced when beads omits timestamps from its diff. Missing
  timestamps are read at the exact reported commit, preserving pull pins.

- Ctrl-C now stops `up`, `provision` and the other commands that show their
  progress in the terminal pane. It was ignored there: the pane puts the
  terminal in raw mode, where Ctrl-C arrives as a key instead of an
  interrupt, so a provision that ran for a long time could not be stopped
  from the keyboard. Stopping only detaches from the instance, where the
  Home Manager switch keeps running; `bivouac provision` attaches to it
  again.
