#!/bin/bash
set -euo pipefail

curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install --no-confirm

# --- login shell
# The account's login shell, decided here and never again: nothing else
# applies it, which is why the `shell` config field cannot change later.
# fish and zsh come from apt so they register in /etc/shells and live at
# /usr/bin, not in a Nix profile that garbage collection or a failed first
# `home-manager switch` could remove.
#
# Guarded as one condition because this script runs under `set -e`: a
# failed install must not abort it before the user exists, which would lock
# everyone out, and must leave the account on bash, never pointing at a
# missing binary.
LOGIN_SHELL=/bin/bash
{{- if ne .Shell "bash"}}
WANT_SHELL=/usr/bin/{{.Shell}}
if DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 update -qq \
  && DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y -qq {{.Shell}} \
  && [ -x "$WANT_SHELL" ] && grep -qx "$WANT_SHELL" /etc/shells && "$WANT_SHELL" -c true; then
  LOGIN_SHELL=$WANT_SHELL
else
  echo "bivouac: could not set up {{.Shell}}; the login shell stays bash" >&2
fi
{{- end}}
# --- end login shell

# {{.Username}} is where everything past this script connects and runs
# home-manager as -- created here, at boot, since this is the one
# point with guaranteed root access before root SSH login is disabled
# below.
useradd --create-home --shell "$LOGIN_SHELL" {{.Username}}

install -d -m 0700 -o {{.Username}} -g {{.Username}} /home/{{.Username}}/.ssh
# DigitalOcean seeds the account's registered SSH keys into root's
# authorized_keys at boot, before this script runs -- reuse them for
# the new user rather than requiring a second key registration.
install -m 0600 -o {{.Username}} -g {{.Username}} /root/.ssh/authorized_keys /home/{{.Username}}/.ssh/authorized_keys

# bivouac is fully automated with no interactive terminal on the
# remote side -- passwordless sudo is required for any future
# automation that needs root, at the same trust level root already had.
echo '{{.Username}} ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/{{.Username}}
chmod 0440 /etc/sudoers.d/{{.Username}}
visudo -cf /etc/sudoers.d/{{.Username}}

# home-manager's declared systemd.user.services (e.g. the docker
# template's dockerd unit) run under the login user's systemd --user
# instance. Without lingering, that instance -- and anything running
# in it -- stops the moment the SSH session that ran `home-manager
# switch` closes, instead of persisting like a real system service.
loginctl enable-linger {{.Username}}

# Root access is provisioning-only from here on: everything past
# cloud-init (home-manager, git, rsync, ssh) connects as
# {{.Username}}. Disabled last, and only once the new user's own
# key-based login is confirmed in place, so a failure anywhere above
# never locks the instance out entirely.
if [ -s /home/{{.Username}}/.ssh/authorized_keys ]; then
  echo 'PermitRootLogin no' > /etc/ssh/sshd_config.d/99-bivouac-disable-root.conf
  sshd -t
  systemctl reload ssh 2>/dev/null || systemctl reload sshd
fi
