{{- /*
The software half of the boot script: Nix and the login shell, nothing
that belongs to one instance. It is shared by a stock launch, which runs it
ahead of the access setup in cloud-init.sh, and by an image build, which
runs it alone as the builder's user data and snapshots the result -- so a
baked launch can skip it without the two drifting apart.
*/ -}}
{{define "bootstrap-script" -}}
#!/bin/bash
set -euo pipefail

{{template "bootstrap" .}}
{{end}}
{{define "bootstrap" -}}
curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install --no-confirm

# --- login shell
# The account's login shell, decided here and never again: nothing else
# applies it, which is why the `shell` config field cannot change later.
# fish and zsh are installed with apt so they register in /etc/shells and
# live at /usr/bin, not in a Nix profile that garbage collection or a failed
# first `home-manager switch` could remove.
#
# Guarded as one condition because this script runs under `set -e`: a
# failed install must not abort it before the user exists, which would lock
# everyone out, and must leave the account on bash, never pointing at a
# missing binary.
LOGIN_SHELL=/bin/bash
{{- if ne .Shell "bash"}}
WANT_SHELL=/usr/bin/{{.Shell}}
{{- if eq .Shell "fish"}}
# Ubuntu 24.04 ships fish 3.7, but the config that runs under it targets
# fish 4, so take fish from the release-4 PPA, which tracks upstream. Its
# own step, and non-fatal: without it the distribution's fish still works.
add-apt-repository -y ppa:fish-shell/release-4 >/dev/null 2>&1 \
  || echo "bivouac: fish 4 PPA unavailable; using the distribution's fish" >&2
{{- end}}
if DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 update -qq \
  && DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y -qq {{.Shell}} \
  && [ -x "$WANT_SHELL" ] && grep -qx "$WANT_SHELL" /etc/shells && "$WANT_SHELL" -c true; then
  LOGIN_SHELL=$WANT_SHELL
else
  echo "bivouac: could not set up {{.Shell}}; the login shell stays bash" >&2
fi
{{- end}}
# --- end login shell
{{- end}}
