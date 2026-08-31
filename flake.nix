{
  description = "cloudlab development environment";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems f;

      # nixpkgs has not packaged Pkl past 0.31.1 (checked via NixHub), but
      # pkl-go's codegen (`pkl.golang`) requires Pkl >= 0.32.0. Fetch
      # Apple's own prebuilt binary release directly until nixpkgs catches
      # up. Hashes captured via `nix-prefetch-url` against each release
      # asset.
      pklVersion = "0.32.1";
      pklAssetName = {
        aarch64-darwin = "pkl-macos-aarch64";
        x86_64-darwin = "pkl-macos-amd64";
        aarch64-linux = "pkl-linux-aarch64";
        x86_64-linux = "pkl-linux-amd64";
      };
      pklHash = {
        aarch64-darwin = "1lyrq75dg84n4rr2c88z7189gvbmqr2xfkj64lv6mc90k8fbagjn";
        x86_64-darwin = "07rdgv95xbnjj7nmb0cfp7cnglhjvpmjqvzn8h0rcis04c1vjx2v";
        aarch64-linux = "0n50vc8dzf10dn2gyxgvakm5jzkwyirp6d5h27wshdd4gpa2svd7";
        x86_64-linux = "049jbzpg27xr9ccm67n22cj07yd8yk2vd6sfj0fss32wm4nvd01i";
      };

      pklFor =
        system: pkgs:
        pkgs.stdenvNoCC.mkDerivation {
          pname = "pkl";
          version = pklVersion;
          src = pkgs.fetchurl {
            url = "https://github.com/apple/pkl/releases/download/${pklVersion}/${pklAssetName.${system}}";
            sha256 = pklHash.${system};
          };
          dontUnpack = true;
          installPhase = ''
            mkdir -p $out/bin
            cp $src $out/bin/pkl
            chmod +x $out/bin/pkl
          '';
        };
    in
    {
      devShells = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go
              (pklFor system pkgs)
            ];
            # Some sandboxed shells restrict writes to arbitrary
            # subdirectories of $HOME (observed: ~/go/pkg/mod, ~/.pkl both
            # blocked with "Operation not permitted" even though they're
            # owned by the current user). Route Go's module cache into the
            # repo itself (gitignored) so `go build`/`go test`/`go generate`
            # work regardless of that restriction.
            shellHook = ''
              export GOPATH="$PWD/.gocache/gopath"
              export GOMODCACHE="$GOPATH/pkg/mod"
              mkdir -p "$GOMODCACHE"
            '';
          };
        }
      );
    };
}
