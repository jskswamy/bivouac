#!/bin/bash
set -euo pipefail

curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install --no-confirm

# home-manager's declared systemd.user.services (e.g. the docker
# template's dockerd unit) run under root's systemd --user instance.
# Without lingering, that instance -- and anything running in it --
# stops the moment the SSH session that ran `home-manager switch`
# closes, instead of persisting like a real system service.
loginctl enable-linger root
