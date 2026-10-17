#!/bin/bash
# Strips the builder of everything that belongs to this one machine or to
# the build, so each Droplet launched from the snapshot gets its own
# identity and only the access its own boot sets up.
#
# Usage: sanitize.sh <username>
#
# SANITIZE_ROOT prefixes every path, so tests can run this against a fake
# tree; with it set, the commands that act on the live system (account
# removal, cloud-init, journald) are printed instead of run.
set -euo pipefail

user=${1:-}
# The builder account build-closure.sh created; the name reaches userdel
# and rm -rf, so it is held to the same rule.
if ! [[ $user =~ ^[a-z][a-z0-9_-]{0,31}$ ]]; then
  echo "sanitize: invalid username '$user'" >&2
  exit 2
fi

root=${SANITIZE_ROOT:-}
gcroot=$root/nix/var/nix/gcroots/bivouac-image/closure
homes=$root/home

run() {
  if [ -n "$root" ]; then
    echo "would run: $*"
  else
    "$@"
  fi
}

# The account and its home: the build user's Nix profile, fetcher cache
# and evaluation leftovers. The build's processes have exited, but a
# straggler would make userdel refuse, and leaving the account is worse.
run pkill -KILL -u "$user" || true
run userdel -r "$user"
rm -rf "${homes:?}/$user"

# Ubuntu's supported preparation for a golden image: drops instance
# state, so the next boot is a new instance and reruns the per-instance
# modules (host keys, user data), plus cloud-init's logs and seed.
run cloud-init clean --logs --machine-id --seed
rm -rf "$root/var/lib/cloud/instances" "$root/var/lib/cloud/instance"

# Packer's temporary key. Emptied rather than removed: DigitalOcean seeds
# the launch's keys into this file at boot, and the boot script copies
# them to the real account from here.
if [ -e "$root/root/.ssh/authorized_keys" ]; then
  : >"$root/root/.ssh/authorized_keys"
fi

# Clones must not share a host identity. cloud-init regenerates the keys
# on the first boot of the new instance.
rm -f "$root"/etc/ssh/ssh_host_*

# Empty, not removed and not "uninitialized": systemd generates a fresh ID
# for an empty file at boot, while "uninitialized" also marks it a first
# boot and re-applies unit presets to an image already configured.
# cloud-init clean --machine-id writes "uninitialized", so this follows it.
if [ -e "$root/etc/machine-id" ]; then
  : >"$root/etc/machine-id"
fi
# Ubuntu links D-Bus's copy to /etc/machine-id; a real file would be a
# second, shared identity.
if [ -f "$root/var/lib/dbus/machine-id" ] && ! [ -L "$root/var/lib/dbus/machine-id" ]; then
  rm -f "$root/var/lib/dbus/machine-id"
fi

# Shell histories and root's caches can hold whatever the build typed or
# fetched.
shopt -s nullglob dotglob
rm -f "$root"/root/.*_history "$homes"/*/.*_history
rm -rf "$root/root/.cache"

# The build's logs can name inputs, hosts and errors. The journal is
# vacuumed by journald itself, since its files are open; the rest is
# truncated rather than deleted, because daemons keep writing to the
# files they opened and expect their ownership and modes.
run journalctl --rotate --vacuum-time=1s
if [ -d "$root/var/log" ]; then
  find "$root/var/log" -path "$root/var/log/journal" -prune -o -type f \
    \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.old' \) -print0 | xargs -0r rm -f
  find "$root/var/log" -path "$root/var/log/journal" -prune -o -type f -print0 | xargs -0r truncate -s 0
fi

# Contents only: the directories' sticky, world-writable modes must stay.
for dir in "$root/tmp" "$root/var/tmp"; do
  if [ -d "$dir" ]; then
    find "$dir" -mindepth 1 -delete
  fi
done

# Check the result rather than trusting each step: a cleanup that quietly
# skipped something would only show up on someone's launched instance.
failed=0
fail() {
  echo "sanitize: check failed: $*" >&2
  failed=1
}
if [ -e "$homes/$user" ]; then fail "the home of $user still exists"; fi
host_keys=("$root"/etc/ssh/ssh_host_*)
if [ ${#host_keys[@]} -ne 0 ]; then fail "SSH host keys remain: ${host_keys[*]}"; fi
if [ -s "$root/root/.ssh/authorized_keys" ]; then fail "root's authorized_keys is not empty"; fi
if [ -s "$root/etc/machine-id" ]; then fail "/etc/machine-id is not empty"; fi
# The closure must outlive this cleanup; without its root, the first
# garbage collection on a launched instance would delete it.
if ! [ -L "$gcroot" ] || ! [ -e "$gcroot" ]; then fail "the closure's GC root is missing"; fi
if [ -z "$root" ] && id "$user" >/dev/null 2>&1; then fail "account $user still exists"; fi
if [ "$failed" -ne 0 ]; then
  exit 1
fi

run sync
echo "sanitize: builder cleaned"
