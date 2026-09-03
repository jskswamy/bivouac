{ pkgs, ... }:
{
  home.username = "root";
  home.homeDirectory = "/root";
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
  systemd.user.services.tailscaled = {
    Unit.Description = "Tailscale daemon";
    Service = {
      ExecStart = "${pkgs.tailscale}/bin/tailscaled --state=/var/lib/tailscale/tailscaled.state";
      Restart = "on-failure";
    };
    Install.WantedBy = [ "default.target" ];
  };
}
