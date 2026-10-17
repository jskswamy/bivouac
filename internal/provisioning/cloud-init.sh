#!/bin/bash
set -euo pipefail

{{if .Baked -}}
# --- login shell
# A baked image already carries the shell it was built with, at the same
# apt path a stock boot installs it to, so only detect it. The same checks
# guard it as on a stock boot: an image that somehow lacks the binary, or
# never registered it, leaves the account on bash rather than pointing it
# at something no one can log into.
LOGIN_SHELL=/bin/bash
{{- if ne .Shell "bash"}}
WANT_SHELL=/usr/bin/{{.Shell}}
if [ -x "$WANT_SHELL" ] && grep -qx "$WANT_SHELL" /etc/shells && "$WANT_SHELL" -c true; then
  LOGIN_SHELL=$WANT_SHELL
else
  echo "bivouac: {{.Shell}} is missing from this image; the login shell stays bash" >&2
fi
{{- end}}
# --- end login shell
{{- else -}}
{{template "bootstrap" .}}
{{- end}}

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
