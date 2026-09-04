{ pkgs, lib, config, ... }:
{
  imports = [ ./common.nix ];

  home.packages = [
    pkgs.docker
    # minikube already provides its own bin/kubectl; a separate
    # pkgs.kubectl here conflicts with it in buildEnv (same path,
    # different derivation).
    pkgs.minikube
  ];

  # dockerd (below) binds its socket group-owned by "docker" -- without
  # membership, every `docker` CLI call would need sudo too. Scoped
  # here rather than cloud-init.sh since it's only needed when this
  # template is actually in use. Runs via sudo since a home-manager
  # activation script runs as this user, not root; cloud-init.sh grants
  # passwordless sudo. Group membership only takes effect for a new
  # login session, which any later `cloudlab ssh`/reconcile already is.
  home.activation.dockerGroup = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    $DRY_RUN_CMD /usr/bin/sudo /usr/sbin/groupadd -f docker
    $DRY_RUN_CMD /usr/bin/sudo /usr/sbin/usermod -aG docker ${config.home.username}
  '';

  # home-manager only puts the docker CLI on PATH -- it doesn't start
  # the daemon. This runs dockerd under the instance user's own
  # systemd --user instance (cloud-init enables lingering for that
  # user so it persists across reboots, not just while an SSH session
  # is open).
  #
  # Runs via sudo: dockerd needs root (cgroups, network namespaces) and
  # writes under /var/lib/docker and /var/run/docker.sock, both
  # root-owned -- this user's systemd --user instance can't do either
  # directly. cloud-init.sh grants it passwordless sudo, so this still
  # starts non-interactively.
  systemd.user.services.docker = {
    Unit.Description = "Docker daemon";
    Service = {
      ExecStart = "/usr/bin/sudo ${pkgs.docker}/bin/dockerd";
      Restart = "on-failure";
    };
    Install.WantedBy = [ "default.target" ];
  };
}
