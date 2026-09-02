{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux = { };
    homeManagerModules.default = { pkgs, ... }: { home.packages = [ pkgs.hello ]; };
  };
}
