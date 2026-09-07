# beads ships prebuilt release tarballs, so this is a fetch and an install
# rather than a Go build.
#
# Pinned to the same upstream release the maintainer's own machine runs
# (overlays/60-beads-latest.nix). nixpkgs ships 1.0.3 against 1.1.2 locally,
# and both machines write one shared Dolt database -- schema skew there is the
# one place a version gap does real damage, so the version is pinned rather
# than tracked. Bumping means editing this file AND that overlay together.
#
# Only the two Linux systems the template flake builds; the overlay's darwin
# entries are irrelevant here.
{
  stdenv,
  fetchzip,
  lib,
}:
let
  version = "1.1.2";
  sources = {
    x86_64-linux = fetchzip {
      url = "https://github.com/steveyegge/beads/releases/download/v${version}/beads_${version}_linux_amd64.tar.gz";
      stripRoot = false;
      hash = "sha256-QUxIc1BRnBGIZ9sLsP5Dobx49x4krV+tLmed3G9h+7Q=";
    };
    aarch64-linux = fetchzip {
      url = "https://github.com/steveyegge/beads/releases/download/v${version}/beads_${version}_linux_arm64.tar.gz";
      stripRoot = false;
      hash = "sha256-JGyantVQRvFNF3ygP7KA+GXYDYTT3CoH8e+fKi2qlzg=";
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
    description = "Dolt-backed issue tracker for coding agents";
    homepage = "https://github.com/steveyegge/beads";
    mainProgram = "bd";
    platforms = builtins.attrNames sources;
    license = lib.licenses.mit;
  };
}
