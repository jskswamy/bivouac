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
if id "{{.Username}}" >/dev/null 2>&1; then
  usermod -s "$LOGIN_SHELL" {{.Username}}
else
  useradd --create-home --shell "$LOGIN_SHELL" {{.Username}}
fi

install -d -m 0700 -o {{.Username}} -g {{.Username}} /home/{{.Username}}/.ssh
# DigitalOcean seeds the account's registered SSH keys into root's
# authorized_keys at boot, before this script runs -- reuse them for
# the new user rather than requiring a second key registration. On
# providers like Google Cloud, the guest agent may have already created
# the user and injected keys; merge them so nothing is lost.
if [ -f /root/.ssh/authorized_keys ]; then
  if [ -f /home/{{.Username}}/.ssh/authorized_keys ]; then
    cat /root/.ssh/authorized_keys >> /home/{{.Username}}/.ssh/authorized_keys
    sort -u /home/{{.Username}}/.ssh/authorized_keys -o /home/{{.Username}}/.ssh/authorized_keys
    chown {{.Username}}:{{.Username}} /home/{{.Username}}/.ssh/authorized_keys
    chmod 0600 /home/{{.Username}}/.ssh/authorized_keys
  else
    install -m 0600 -o {{.Username}} -g {{.Username}} /root/.ssh/authorized_keys /home/{{.Username}}/.ssh/authorized_keys
  fi
fi

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
  # Ubuntu 24.04 starts sshd per connection through ssh.socket, so until
  # someone first connects there is no privilege separation directory,
  # which the config check below insists on, and no running service to
  # reload. A baked image reaches this seconds after boot, before any
  # connection; a socket-started sshd reads the new config on the next
  # one anyway.
  mkdir -p /run/sshd
  sshd -t
  systemctl try-reload-or-restart ssh 2>/dev/null || systemctl try-reload-or-restart sshd
fi
