{ pkgs, ... }:
{
  imports = [ ./common.nix ];

  home.packages = [
    pkgs.docker
    # minikube already provides its own bin/kubectl; a separate
    # pkgs.kubectl here conflicts with it in buildEnv (same path,
    # different derivation).
    pkgs.minikube
  ];
}
