{ pkgs, lib, ... }:
let
  # The instance's own non-root user (see internal/identity.RemoteUser
  # and cloud-init.sh, which creates it) -- read from the SSH session's
  # own environment rather than hardcoded, since this same checked-in
  # template is shared across every instance, each provisioned under a
  # different user. Requires --impure on `home-manager switch` (see
  # internal/reconcile/reconcile.go).
  username = builtins.getEnv "USER";

  # Pinned to gpakosz/.tmux's master branch HEAD at the time this was
  # added (the repo has no tags/releases to pin to instead). sha256
  # captured via `nix-prefetch-url --unpack
  # https://github.com/gpakosz/.tmux/archive/<rev>.tar.gz` -- bump
  # both rev and sha256 together the same way if a newer commit is
  # ever wanted.
  tmuxDotfiles = pkgs.fetchFromGitHub {
    owner = "gpakosz";
    repo = ".tmux";
    rev = "58a3dcc0d718ec0fa1c0d5a2fddd640a1ad7a5b7";
    sha256 = "0zky4qkndrs645xnxh6498zc8yj7y581sg72hh0h7b31a5jxng30";
  };
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
    pkgs.tmux
  ];

  programs.fish.enable = true;
  programs.starship.enable = true;

  # gpakosz/.tmux's own config, used exactly as upstream ships it --
  # both files symlinked straight from the fetched repo, nothing
  # hand-copied or reproduced. .tmux.conf.local is wrapped in
  # mkDefault so a personal base.pkl-declared flake module can set
  # its own home.file.".tmux.conf.local".source later and win -- the
  # same personal-customization path packages/flakes already use (see
  # docs/config.md), no new cloudlab.pkl field needed for this.
  home.file.".tmux.conf".source = "${tmuxDotfiles}/.tmux.conf";
  home.file.".tmux.conf.local".source = lib.mkDefault "${tmuxDotfiles}/.tmux.conf.local";

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
