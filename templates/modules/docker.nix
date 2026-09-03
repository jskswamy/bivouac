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

  # home-manager only puts the docker CLI on PATH -- it doesn't start
  # the daemon. This runs dockerd under root's systemd --user instance
  # (cloud-init enables lingering for root so it persists across
  # reboots, not just while an SSH session is open).
  systemd.user.services.docker = {
    Unit.Description = "Docker daemon";
    Service = {
      ExecStart = "${pkgs.docker}/bin/dockerd";
      Restart = "on-failure";
    };
    Install.WantedBy = [ "default.target" ];
  };
}
