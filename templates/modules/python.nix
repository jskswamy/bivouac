{ pkgs, ... }:
{
  imports = [ ./common.nix ];

  home.packages = [
    pkgs.python312
    pkgs.uv
  ];
}
