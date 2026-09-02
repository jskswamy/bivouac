{
  description = "cloudlab template catalog";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      home-manager,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      templateNames = [
        "python"
        "docker"
      ];

      homeManagerModules = {
        python = import ./modules/python.nix;
        docker = import ./modules/docker.nix;
      };

      mkConfig =
        system: name:
        home-manager.lib.homeManagerConfiguration {
          pkgs = nixpkgs.legacyPackages.${system};
          modules = [ homeManagerModules.${name} ];
        };
    in
    {
      inherit homeManagerModules;

      homeConfigurations = nixpkgs.lib.listToAttrs (
        nixpkgs.lib.concatMap (
          system:
          map (name: {
            name = "${name}-${system}";
            value = mkConfig system name;
          }) templateNames
        ) systems
      );
    };
}
