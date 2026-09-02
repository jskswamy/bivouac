{
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.cli = nixpkgs.legacyPackages.x86_64-linux.hello;
  };
}
