{
  description = "cloudlab development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    git-hooks.url = "github:cachix/git-hooks.nix";
    direnv-instant.url = "github:Mic92/direnv-instant";
  };

  outputs =
    {
      self,
      nixpkgs,
      git-hooks,
      direnv-instant,
    }:
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
      perSystem = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          pre-commit-check = git-hooks.lib.${system}.run {
            src = ./.;
            hooks = {
              # Nix
              nixfmt-rfc-style.enable = true;
              # statix.enable disabled: nixpkgs-unstable's statix 0.5.8
              # currently fails its own build (broken upstream snapshot
              # test in bool_comparison, unrelated to this repo) — retry
              # once nixpkgs picks up a fixed statix.
              deadnix = {
                enable = true;
                settings.edit = true;
                settings.noLambdaPatternNames = true; # preserve 'self' in flake outputs
              };

              # Go
              gofmt.enable = true;
              golangci-lint = {
                enable = true;
                # golangci-lint shells out to go; nix flake check's sandbox
                # has no PATH go otherwise.
                extraPackages = [ pkgs.go ];
              };

              # Secrets
              trufflehog.enable = true;

              # General hygiene
              check-yaml.enable = true;
              check-merge-conflicts.enable = true;
              check-added-large-files.enable = true;
              trim-trailing-whitespace.enable = true;
            };
          };
        in
        {
          inherit pre-commit-check;
          devShell = pkgs.mkShell {
            packages = [
              pkgs.go
              pkgs.gopls
              (pklFor system pkgs)
              direnv-instant.packages.${system}.default
            ]
            ++ pre-commit-check.enabledPackages;
            # Some sandboxed shells restrict writes to arbitrary
            # subdirectories of $HOME (observed: ~/go/pkg/mod, ~/.pkl both
            # blocked with "Operation not permitted" even though they're
            # owned by the current user). Route Go's module cache and
            # Pkl's package cache (internal/config.pklCacheDir reads
            # $XDG_CACHE_HOME) into the repo itself unconditionally,
            # rather than guessing whether the real $HOME is usable.
            # ~/.gitconfig, ~/.ssh, etc. are never touched. Everything
            # under .gocache/ is gitignored.
            shellHook = ''
              ${pre-commit-check.shellHook}
              export GOPATH="$PWD/.gocache/gopath"
              export GOMODCACHE="$GOPATH/pkg/mod"
              export XDG_CACHE_HOME="$PWD/.gocache/xdg-cache"
              export GOFLAGS="-modcacherw"
              mkdir -p "$GOMODCACHE" "$XDG_CACHE_HOME"
            '';
          };
        }
      );
    in
    {
      devShells = nixpkgs.lib.mapAttrs (_: v: { default = v.devShell; }) perSystem;
      checks = nixpkgs.lib.mapAttrs (_: v: { inherit (v) pre-commit-check; }) perSystem;
    };
}
