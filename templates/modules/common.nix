{ pkgs, lib, config, ... }:
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

  moshiHook = pkgs.callPackage ./moshi-hook-pkg.nix { };
in
{
  options.cloudlab.tailscale = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = "Enable Tailscale daemon on this instance";
  };

  config.home.username = username;
  config.home.homeDirectory = builtins.getEnv "HOME";
  config.home.stateVersion = "25.11";

  config.home.packages = [
    pkgs.git
    pkgs.age
    pkgs.devbox
    # Started as a headless server by the systemd --user unit below,
    # rather than left to launch on demand.
    pkgs.herdr
    # Lets the getmoshi.app mobile client (SSH & Mosh from iOS/Android)
    # connect to this instance -- moshi itself is a client-side app,
    # this host only needs the mosh server side it speaks to.
    pkgs.mosh
    # The `moshi`/`moshi-hook` binaries themselves (packaged in
    # ./moshi-hook-pkg.nix). Unlike mosh above, this one *does* need a
    # persistent daemon on the host -- see the activation script below --
    # so Moshi's iOS/Android app can pair with it and drive agent hooks.
    moshiHook
    pkgs.tailscale
    pkgs.tmux
  ];

  config.programs.fish.enable = true;
  config.programs.starship.enable = true;

  # gpakosz/.tmux's own config, used exactly as upstream ships it --
  # both files symlinked straight from the fetched repo, nothing
  # hand-copied or reproduced. .tmux.conf.local is wrapped in
  # mkDefault so a personal base.pkl-declared flake module can set
  # its own home.file.".tmux.conf.local".source later and win -- the
  # same personal-customization path packages/flakes already use (see
  # docs/config.md), no new cloudlab.pkl field needed for this.
  config.home.file.".tmux.conf".source = "${tmuxDotfiles}/.tmux.conf";
  config.home.file.".tmux.conf.local".source = lib.mkDefault "${tmuxDotfiles}/.tmux.conf.local";

  # The Moshi mobile client supports herdr out of the box, so a phone
  # paired via `cloudlab pair` expects a herdr server to already be
  # running here. Left to itself herdr only starts one on demand at
  # first attach, which means it exists solely once someone has attached
  # from a desktop (`cloudlab herdr`, i.e. `herdr --remote`) -- a
  # freshly paired phone would find nothing to connect to.
  #
  # Starting it at boot also decouples the server's lifetime from
  # whichever SSH session happened to spawn it, so the persistent
  # sessions and agent panes it holds survive a disconnect.
  #
  # No sudo here, unlike tailscaled and the docker template's dockerd:
  # herdr is a terminal workspace manager that runs entirely as this
  # user, keeping its socket, config and logs under ~/.config/herdr.
  # cloud-init's `loginctl enable-linger` is what keeps this user's
  # systemd instance -- and so this server -- alive between logins.
  config.systemd.user.services.herdr = {
    Unit.Description = "Herdr headless server";
    Service = {
      ExecStart = "${pkgs.herdr}/bin/herdr server";
      Restart = "on-failure";
    };
    Install.WantedBy = [ "default.target" ];
  };

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
  #
  # Only enabled when cloudlab.tailscale is true (set by provisioning
  # when user sets tailscale: true in cloudlab.pkl) -- otherwise
  # tailscaled starting during provisioning would block SSH.
  config.systemd.user.services.tailscaled = lib.mkIf config.cloudlab.tailscale {
    Unit.Description = "Tailscale daemon";
    Service = {
      ExecStart = "/usr/bin/sudo ${pkgs.tailscale}/bin/tailscaled --state=/var/lib/tailscale/tailscaled.state";
      Restart = "on-failure";
    };
    Install.WantedBy = [ "default.target" ];
  };

  # moshi-hook writes and enables its own systemd --user unit (see
  # `moshi-hook service status` after this runs) -- no hand-written
  # systemd.user.services entry needed, and no sudo either: unlike
  # tailscaled/dockerd above it doesn't touch root-owned state. Re-running
  # on every activation is fine, `service install` is idempotent.
  config.home.activation.moshiHookService = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    $DRY_RUN_CMD ${moshiHook}/bin/moshi-hook service install
  '';
}
