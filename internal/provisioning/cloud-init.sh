#!/bin/bash
set -euo pipefail

curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install --no-confirm

# {{.Username}} is where everything past this script connects and runs
# home-manager as -- created here, at boot, since this is the one
# point with guaranteed root access before root SSH login is disabled
# below.
useradd --create-home --shell /bin/bash {{.Username}}

install -d -m 0700 -o {{.Username}} -g {{.Username}} /home/{{.Username}}/.ssh
# DigitalOcean seeds the account's registered SSH keys into root's
# authorized_keys at boot, before this script runs -- reuse them for
# the new user rather than requiring a second key registration.
install -m 0600 -o {{.Username}} -g {{.Username}} /root/.ssh/authorized_keys /home/{{.Username}}/.ssh/authorized_keys

# cloudlab is fully automated with no interactive terminal on the
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
# cloud-init (home-manager, rsync, watch, ssh) connects as
# {{.Username}}. Disabled last, and only once the new user's own
# key-based login is confirmed in place, so a failure anywhere above
# never locks the instance out entirely.
if [ -s /home/{{.Username}}/.ssh/authorized_keys ]; then
  echo 'PermitRootLogin no' > /etc/ssh/sshd_config.d/99-cloudlab-disable-root.conf
  sshd -t
  systemctl reload ssh 2>/dev/null || systemctl reload sshd
fi
