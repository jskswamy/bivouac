# moshi-hook isn't in nixpkgs -- it's a Go binary Jetify^Wgetmoshi.app ships
# only via Homebrew (macOS) or a curl-able CDN tarball (Linux/Windows). This
# packages the Linux CDN release so it's reproducible like everything else
# home-manager installs, instead of shelling out to install.sh.
#
# Version/hashes pinned from https://cdn.getmoshi.app/hook/latest/version.txt
# and .../hook/<version>/checksums.txt. Bump by updating `version` below and
# re-running, per arch:
#   nix-prefetch-url --type sha256 \
#     https://cdn.getmoshi.app/hook/v<version>/moshi-hook_Linux_<arch>.tar.gz
{ stdenvNoCC, fetchurl }:
let
  version = "0.3.18";

  # checksums.txt keys releases by Go's GOARCH-ish "arm64"/"x86_64", not the
  # "aarch64-linux" nixpkgs system string -- map explicitly rather than
  # string-munging pkgs.stdenv.hostPlatform.system.
  assets = {
    x86_64-linux = {
      arch = "x86_64";
      sha256 = "03f4l2gx2k6nlzdaw00cf78sjf0hvh7fyknmv2nmv9iscg2jrr84";
    };
    aarch64-linux = {
      arch = "arm64";
      sha256 = "1xvqwk5rj6nwvka3y9283i8xs56s0f6256kygbsmwla8vljf36dd";
    };
  };
  asset =
    assets.${stdenvNoCC.hostPlatform.system}
      or (throw "moshi-hook: no CDN release for ${stdenvNoCC.hostPlatform.system}");
in
stdenvNoCC.mkDerivation {
  pname = "moshi-hook";
  inherit version;

  src = fetchurl {
    url = "https://cdn.getmoshi.app/hook/v${version}/moshi-hook_Linux_${asset.arch}.tar.gz";
    sha256 = asset.sha256;
  };

  # The tarball's top level is README.md + docs/ + moshi-hook (no wrapping
  # dir). stdenv's unpacker only looks at directories when guessing
  # sourceRoot, finds the lone "docs" dir, and cds into it -- stranding the
  # binary one level up. Pin sourceRoot so it stays at the extraction root.
  sourceRoot = ".";

  # Statically-linked Go binary -- no autoPatchelf/glibc dance needed.
  dontConfigure = true;
  dontBuild = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 moshi-hook $out/bin/moshi-hook
    ln -s moshi-hook $out/bin/moshi
    runHook postInstall
  '';

  meta = {
    description = "Daemon/CLI that lets the getmoshi.app mobile app SSH/Mosh into this host";
    homepage = "https://getmoshi.app";
    platforms = builtins.attrNames assets;
  };
}
