## Unreleased

### Fixed

- Ctrl-C now stops `up`, `provision` and the other commands that show their
  progress in the terminal pane. It was ignored there: the pane puts the
  terminal in raw mode, where Ctrl-C arrives as a key instead of an
  interrupt, so a provision that ran for a long time could not be stopped
  from the keyboard. Stopping only detaches from the instance, where the
  Home Manager switch keeps running; `bivouac provision` attaches to it
  again.
