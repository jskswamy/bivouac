#!/bin/bash
# Waits for the builder's user data (scripts/bootstrap.sh) to finish and
# checks it produced what the image will be tagged as carrying.
#
# Usage: wait-bootstrap.sh <shell>
#
# WAIT_ROOT prefixes every path checked, so tests can point it at a fake
# tree. BOOTSTRAP_TIMEOUT (seconds) bounds the wait.
set -euo pipefail

shell=${1:-}
root=${WAIT_ROOT:-}

# Interpolated into a path below; a name with a slash could point the
# check at any file.
if ! [[ $shell =~ ^[a-z][a-z0-9-]*$ ]]; then
  echo "wait-bootstrap: invalid shell name '$shell'" >&2
  exit 2
fi

# Packer connects as soon as sshd answers, which is long before the Nix
# installer in user data has finished. Bounded, so a bootstrap that hangs
# fails the build instead of holding a paid builder open until Packer's
# own timeout.
rc=0
status=$(timeout "${BOOTSTRAP_TIMEOUT:-1800}" cloud-init status --wait 2>&1) || rc=$?
echo "$status"
# 2 is cloud-init's "done, with recoverable warnings", which a stock
# Ubuntu boot can report for reasons unrelated to our script; a failed
# user-data script is 1. Both the code and the text are checked, so a
# cloud-init that reports an error but exits 0 is not taken as done.
if { [ "$rc" -ne 0 ] && [ "$rc" -ne 2 ]; } || ! grep -q 'status: done' <<<"$status"; then
  echo "wait-bootstrap: cloud-init did not finish cleanly (exit $rc)" >&2
  exit 1
fi

nix=$root/nix/var/nix/profiles/default/bin/nix
if ! [ -x "$nix" ] || ! "$nix" --version; then
  echo "wait-bootstrap: Nix is not installed at /nix/var/nix/profiles/default/bin/nix" >&2
  exit 1
fi

# The bootstrap falls back to bash when the shell will not install,
# because on a live instance a locked-out user is worse than the wrong
# shell. Here that fallback would produce an image tagged with a shell it
# does not have, so it is a build failure. These are the checks the
# bootstrap and the baked boot path make before trusting the binary.
want=/usr/bin/$shell
if [ "$shell" = bash ]; then
  want=/bin/bash
fi
if ! [ -x "$root$want" ] || ! grep -qx "$want" "$root/etc/shells" || ! "$root$want" -c true; then
  echo "wait-bootstrap: $shell did not install: $want is missing, not registered in /etc/shells, or does not run" >&2
  exit 1
fi
echo "wait-bootstrap: bootstrap complete with Nix and $want"
