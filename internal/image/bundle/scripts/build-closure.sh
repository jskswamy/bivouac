#!/bin/bash
# Builds the configured Home Manager activation package into the Nix store
# and roots it against garbage collection, without activating it.
#
# Usage: build-closure.sh <username>
#
# BUILD_ROOT prefixes every path used, so tests can point it at a fake
# tree.
set -euo pipefail

user=${1:-}
# The same rule Bivouac applies to the launch username. Checked before
# anything runs: the name becomes an account and goes into commands.
if ! [[ $user =~ ^[a-z][a-z0-9_-]{0,31}$ ]]; then
  echo "build-closure: invalid username '$user'" >&2
  exit 2
fi

root=${BUILD_ROOT:-}
image_dir=$root/var/lib/bivouac-image
metadata=$image_dir/metadata.json
# By absolute path: Packer's shell provisioner is not a login shell, so
# root's PATH here does not include the Nix profile.
nix=$root/nix/var/nix/profiles/default/bin/nix
nix_store=$root/nix/var/nix/profiles/default/bin/nix-store
gcroot=$root/nix/var/nix/gcroots/bivouac-image/closure
# useradd's default base directory, where the launch's account lives too.
homes=/home

# python3 rather than jq: cloud-init is written in Python, so every
# Ubuntu cloud image has it, while jq is not guaranteed.
flake_ref=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["flake_ref"])' "$metadata")

# Split the way home-manager does. With no fragment, a launch's switch
# tries "<user>@<host>" and then "<user>"; the builder's host name is not
# the launch's, so only the plain user name can name the same output.
url=${flake_ref%%#*}
name=$user
if [[ $flake_ref == *#* ]]; then
  name=${flake_ref#*#}
fi
# Quoted into a Nix attribute path below.
if ! [[ $name =~ ^[A-Za-z0-9._@+-]+$ ]]; then
  echo "build-closure: unsupported configuration name '$name' in $flake_ref" >&2
  exit 2
fi
# The bundle refers to its rendered flake bundle-relatively, so it can be
# run from anywhere; only this builder knows where Packer uploaded it.
if [ "$url" = "path:./nix" ]; then
  url=path:$image_dir/nix
  chmod -R a+rX "$image_dir/nix"
fi
installable="$url#homeConfigurations.\"$name\".activationPackage"

# Built as an account named exactly like the launch user, because the
# template reads $USER and $HOME (hence --impure) into home.username and
# home.homeDirectory: any other name builds a different package, sharing
# only the plain package paths with the one the launch activates.
useradd --create-home --shell /bin/bash "$user"

# A clean login environment as that user: nothing from Packer's session
# leaks into the evaluation, and nix.sh from /etc/profile sets up the
# daemon connection the way a real login does. cwd is / because root's
# home is unreadable to the account.
as_user() {
  (cd / && runuser -u "$user" -- env -i HOME="$homes/$user" USER="$user" LOGNAME="$user" PATH=/usr/bin:/bin \
    bash -lc "$@")
}

# reconcile's switch flags minus -b, which only matters when files are
# written into a home. `nix build` of the activation package evaluates and
# builds everything a switch would, but runs none of its activation
# scripts: nothing starts services, joins networks or writes a home.
# shellcheck disable=SC2016 # expanded by the build user's shell, not here
out=$(as_user '"$1" build --impure --no-write-lock-file --refresh --no-link --print-out-paths "$2"' \
  bash "$nix" "$installable")
closure=$(tail -n 1 <<<"$out")
if [[ $closure != /nix/store/* ]]; then
  echo "build-closure: nix build printed no store path: $out" >&2
  exit 1
fi

# --no-link left no result symlink and nothing else refers to the
# closure, so without a root of its own the first `nix store gc` on a
# launched instance would collect it. A link under gcroots/ is a direct
# root, independent of any home or symlink cleanup removes.
mkdir -p "$(dirname "$gcroot")"
"$nix_store" --add-root "$gcroot" --realise "$closure" >/dev/null

# Without --refresh, so the fetcher serves the inputs this build just
# resolved rather than re-resolving a floating ref that may have moved
# since; the record then names what was built.
# shellcheck disable=SC2016 # as above
flake_metadata=$(as_user '"$1" flake metadata --json --no-write-lock-file "$2"' bash "$nix" "$url")
merge=$(
  cat <<'EOF'
import json, os, sys

path, closure = sys.argv[1], sys.argv[2]
flake = json.loads(sys.argv[3])
with open(path) as f:
    meta = json.load(f)
# Revisions only: locked inputs say what was built without carrying
# anything from the build user's environment.
meta["revisions"] = {
    "url": flake.get("url"),
    "revision": flake.get("revision"),
    "last_modified": flake.get("lastModified"),
    "inputs": {
        name: node["locked"]
        for name, node in flake.get("locks", {}).get("nodes", {}).items()
        if "locked" in node
    },
}
meta["closure"] = closure
tmp = path + ".tmp"
with open(tmp, "w") as f:
    json.dump(meta, f, indent=2)
    f.write("\n")
os.chmod(tmp, 0o644)
os.replace(tmp, path)
EOF
)
python3 -c "$merge" "$metadata" "$closure" "$flake_metadata"
echo "build-closure: built and rooted $closure"
