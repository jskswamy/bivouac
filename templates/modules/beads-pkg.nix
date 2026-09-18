# beads ships prebuilt release tarballs, so this is a fetch and an install
# rather than a Go build.
#
# Pinned to the same upstream release the maintainer's own machine runs.
# nixpkgs tracks a much older release under a different version scheme, and
# both machines write one shared Dolt database -- schema skew there is the
# one place a version gap does real damage, so the version is pinned rather
# than tracked.
#
# Moved from github.com/steveyegge/beads to github.com/gastownhall/beads
# upstream; both the org and the version need updating together.
#
# fetchzip's hash is of the *unpacked* tree, not the downloaded tarball --
# `nix-prefetch-url --unpack` (or `nix store prefetch-file --unpack`), never
# a plain checksum from the release's checksums.txt. A prior bump here used
# the raw-tarball hash, which cross-checked fine against checksums.txt but
# still failed the actual build, since fetchzip never hashes that value.
#
# Only the two Linux systems the template flake builds.
{
  stdenv,
  fetchzip,
  lib,
}:
let
  version = "1.3.0";
  sources = {
    x86_64-linux = fetchzip {
      url = "https://github.com/gastownhall/beads/releases/download/v${version}/beads_${version}_linux_amd64.tar.gz";
      stripRoot = false;
      hash = "sha256-Ie9TD27mGQd3ctj5FsyDpUC0x2X5eaeNjffXqppQ19U=";
    };
    aarch64-linux = fetchzip {
      url = "https://github.com/gastownhall/beads/releases/download/v${version}/beads_${version}_linux_arm64.tar.gz";
      stripRoot = false;
      hash = "sha256-WCHyEIS+aHr52ItjH88EACMYnFIkxqBt/J+1OC7VWyE=";
    };
  };
  src =
    sources.${stdenv.hostPlatform.system}
      or (throw "beads: no release for ${stdenv.hostPlatform.system}");
in
stdenv.mkDerivation {
  pname = "beads";
  inherit version src;

  # fetchzip already unpacked it; $src is the extracted tree.
  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 $src/bd $out/bin/bd
    runHook postInstall
  '';

  meta = {
    description = "Lightweight memory system for AI coding agents with graph-based issue tracking";
    homepage = "https://github.com/gastownhall/beads";
    mainProgram = "bd";
    platforms = builtins.attrNames sources;
    license = lib.licenses.mit;
  };
}
