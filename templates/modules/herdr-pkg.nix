# herdr ships raw release binaries rather than archives, so this is a fetch
# and an install with no unpack step.
#
# Pinned to the same upstream release the maintainer's own machine runs.
# nixpkgs has never carried a version this recent, so tracking nixpkgs
# cannot close the gap here -- and the gap is the problem: `herdr machine
# add` inspects the server on the instance and, when it is not a version
# the local client can talk to, stops it and deploys its own copy under
# ~/.local/bin, outside nix and outside the systemd unit common.nix
# installs. Matching versions is what keeps the instance's herdr the one
# cloudlab put there.
#
# Bumping means matching whatever the maintainer's own machine is on, since
# a client newer than the instance is exactly the state that triggers the
# takeover.
#
# Only the two Linux systems the template flake builds.
{
  stdenv,
  fetchurl,
  lib,
}:
let
  version = "0.9.1";
  sources = {
    x86_64-linux = fetchurl {
      url = "https://github.com/herdrdev/herdr/releases/download/v${version}/herdr-linux-x86_64";
      hash = "sha256-KgL+0WvrZR7wBuHUPwSPZSyk3FitBTzS1ERQVj1cVLc=";
    };
    aarch64-linux = fetchurl {
      url = "https://github.com/herdrdev/herdr/releases/download/v${version}/herdr-linux-aarch64";
      hash = "sha256-9Mz03nRfLLmjmpg+m6NwPa1Q7CpY3qgwJs6rchu9jZ4=";
    };
  };
  src =
    sources.${stdenv.hostPlatform.system}
      or (throw "herdr: no release for ${stdenv.hostPlatform.system}");
in
stdenv.mkDerivation {
  pname = "herdr";
  inherit version src;

  # $src is the binary itself, not an archive.
  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 $src $out/bin/herdr
    runHook postInstall
  '';

  meta = {
    description = "Terminal workspace manager for AI coding agents";
    homepage = "https://herdr.dev";
    mainProgram = "herdr";
    platforms = builtins.attrNames sources;
    license = lib.licenses.asl20;
  };
}
