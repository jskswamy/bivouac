{ pkgs, ... }:
let
  # The instance's own non-root user (see internal/identity.RemoteUser
  # and cloud-init.sh, which creates it) -- read from the SSH session's
  # own environment rather than hardcoded, since this same checked-in
  # template is shared across every instance, each provisioned under a
  # different user. Requires --impure on `home-manager switch` (see
  # internal/reconcile/reconcile.go).
  username = builtins.getEnv "USER";
in
{
  home.username = username;
  home.homeDirectory = builtins.getEnv "HOME";
  home.stateVersion = "25.11";

  home.packages = [
    pkgs.git
    pkgs.age
    # Self-manages its own server lifecycle (launches/attaches on
    # demand, per `herdr --help`) -- no systemd unit needed, unlike
    # tailscaled below.
    pkgs.herdr
    pkgs.tailscale
  ];

  programs.fish.enable = true;
  programs.starship.enable = true;

  # Same reasoning as the docker template's dockerd unit: a real
  # daemon needs to actually be running, not just installed. Requires
  # a one-time `tailscale up` to join a tailnet (no auth key wired in
  # yet -- that needs the not-yet-implemented sops-nix secrets story).
  #
  # Runs via sudo: tailscaled needs root (it creates a tun network
  # interface) and writes its state under /var/lib/tailscale, a
  # root-owned path -- this user's systemd --user instance can't do
  # either directly. cloud-init.sh grants it passwordless sudo, so this
  # still starts non-interactively.
  systemd.user.services.tailscaled = {
    Unit.Description = "Tailscale daemon";
    Service = {
      ExecStart = "/usr/bin/sudo ${pkgs.tailscale}/bin/tailscaled --state=/var/lib/tailscale/tailscaled.state";
      Restart = "on-failure";
    };
    Install.WantedBy = [ "default.target" ];
  };
}
