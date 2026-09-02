{ pkgs, ... }:
{
  imports = [ ./common.nix ];

  home.packages = [
    pkgs.docker
    pkgs.minikube
    pkgs.kubectl
  ];
}
