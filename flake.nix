{
  description = "bivouac development environment";

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

      # nixpkgs is currently behind the Go point release that fixes the
      # standard-library CVEs govulncheck reports against bivouac's HTTP/TLS
      # call paths. Use the official binary toolchain until nixpkgs catches up.
      goVersion = "1.26.9";
      goAssetName = {
        aarch64-darwin = "go${goVersion}.darwin-arm64.tar.gz";
        x86_64-darwin = "go${goVersion}.darwin-amd64.tar.gz";
        aarch64-linux = "go${goVersion}.linux-arm64.tar.gz";
        x86_64-linux = "go${goVersion}.linux-amd64.tar.gz";
      };
      goHash = {
        aarch64-darwin = "sha256-+bt8CgJQbF2b8NHrH37mx2hPhErklVgwigQnuDDgIsw=";
        x86_64-darwin = "sha256-AMKePUyFYvVHQQ2qGNKVm2Qb1pGrAMSlJeqBegJT1L0=";
        aarch64-linux = "sha256-Spc3PUn8rNzzaU/qNopQCwDuPpY5dPPnUUEycXYy8FI=";
        x86_64-linux = "sha256-QtFYtNj3thrAqDBWfJQKhgmPt6rFLkZ6Xr7APvXML40=";
      };
      goOS = {
        aarch64-darwin = "darwin";
        x86_64-darwin = "darwin";
        aarch64-linux = "linux";
        x86_64-linux = "linux";
      };
      goArch = {
        aarch64-darwin = "arm64";
        x86_64-darwin = "amd64";
        aarch64-linux = "arm64";
        x86_64-linux = "amd64";
      };
      goFor =
        system: pkgs:
        pkgs.stdenvNoCC.mkDerivation {
          pname = "go";
          version = goVersion;
          GOOS = goOS.${system};
          GOARCH = goArch.${system};
          CGO_ENABLED = 0;
          src = pkgs.fetchurl {
            url = "https://go.dev/dl/${goAssetName.${system}}";
            hash = goHash.${system};
          };
          installPhase = ''
            mkdir -p $out
            cp -R . $out
          '';
        };
      perSystem = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          goTool = goFor system pkgs;
          pre-commit-check = git-hooks.lib.${system}.run {
            src = ./.;
            hooks = {
              # Nix
              # nixfmt (not nixfmt-rfc-style, now an alias of the same
              # thing since nixfmt 1.0 made RFC 166 style the default).
              nixfmt.enable = true;
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
                # golangci-lint shells out to go; the official Go toolchain
                # also needs Nix's CA bundle to fetch modules inside checks.
                # The hook environment does not put a C compiler on PATH, so
                # keep Linux stdlib package loading out of cgo just as the
                # govulncheck hook does below.
                entry = "env CGO_ENABLED=0 SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt PATH=${goTool}/bin:$PATH ${pkgs.golangci-lint}/bin/golangci-lint run";
                pass_filenames = false;
              };

              # Security
              trufflehog.enable = true;
              gosec = {
                enable = true;
                name = "gosec";
                description = "Go security linter (SAST)";
                # gosec shells out to go for package loading (same PATH gap
                # as golangci-lint above); .gocache is our own local GOPATH,
                # not project source, and inflates results if scanned.
                entry = "env CGO_ENABLED=0 SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt PATH=${goTool}/bin:$PATH ${pkgs.gosec}/bin/gosec -exclude-dir=.gocache ./...";
                files = "\\.go$";
                pass_filenames = false;
              };
              govulncheck = {
                enable = true;
                name = "govulncheck";
                description = "Go dependency vulnerability scanner";
                # Needs network access to fetch the vuln DB from
                # vuln.go.dev -- fine in CI (sandbox = false) and in a
                # typical local `nix flake check`/pre-commit run, but will
                # fail offline.
                #
                # CGO_ENABLED=0 because this hook runs with PATH narrowed to
                # Go's own bin, so there is no C compiler on it. Linux
                # stdlib builds os/user and net through cgo by default, so
                # the scan died with `cgo: C compiler "gcc" not found` on
                # ubuntu while passing on macOS. bivouac imports no cgo,
                # so turning it off scans exactly the same code.
                entry = "env CGO_ENABLED=0 SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt PATH=${goTool}/bin:$PATH ${pkgs.govulncheck}/bin/govulncheck ./...";
                files = "\\.go$";
                pass_filenames = false;
              };

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
          package = pkgs.buildGoModule.override { go = goTool; } {
            pname = "bivouac";
            version = self.shortRev or self.dirtyShortRev or "dev";
            src = self;
            vendorHash = "sha256-NfjYNNlyqsKGdXPSiu+YSQrbZhtkEEubqwcODWtpyr0=";
            doCheck = false;

            ldflags = [
              "-s"
              "-w"
              "-X main.version=${self.shortRev or self.dirtyShortRev or "dev"}"
              "-X main.commit=${self.shortRev or self.dirtyShortRev or "none"}"
            ];

            meta = {
              description = "Disposable remote workspaces for coding agents";
              homepage = "https://github.com/jskswamy/bivouac";
              mainProgram = "bivouac";
            };
          };
          devShell = pkgs.mkShell {
            packages = [
              goTool
              pkgs.gopls
              pkgs.rsync
              pkgs.mutagen
              # internal/secrets shells out to sops, and its tests (plus
              # cmd's and internal/lifecycle's) build real encrypted
              # fixtures with age-keygen. Same call as beads-pkg.nix
              # below: without these in the shell the suite fails in the
              # environment CONTRIBUTING.md tells you to use, which is
              # worse than skipping.
              pkgs.sops
              pkgs.age
              (pklFor system pkgs)
              direnv-instant.packages.${system}.default
            ]
            # The same derivation the instances get, so the beads round-trip
            # test in internal/beads exercises a real `bd` in CI rather than
            # skipping everywhere. Linux only: beads-pkg.nix deliberately has
            # no darwin entries (the template flake builds neither), so this
            # would throw on a Mac -- there the round-trip test skips.
            ++ nixpkgs.lib.optional (nixpkgs.lib.hasSuffix "-linux" system) (
              pkgs.callPackage ./templates/modules/beads-pkg.nix { }
            )
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
              export SSL_CERT_FILE="${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
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
      packages = nixpkgs.lib.mapAttrs (_: v: { default = v.package; }) perSystem;
      devShells = nixpkgs.lib.mapAttrs (_: v: { default = v.devShell; }) perSystem;
      checks = nixpkgs.lib.mapAttrs (_: v: { inherit (v) pre-commit-check; }) perSystem;
    };
}
