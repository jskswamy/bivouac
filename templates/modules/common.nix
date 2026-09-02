{ pkgs, ... }:
{
  home.username = "root";
  home.homeDirectory = "/root";
  home.stateVersion = "25.11";

  home.packages = [
    pkgs.git
    pkgs.age
  ];
}
