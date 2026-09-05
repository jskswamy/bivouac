package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/secrets"
)

// JoinTailscale joins the instance at ip (connecting as user) to the
// tailnet: decrypts the auth key from the local secrets file just in
// time, resolves the instance's own $XDG_RUNTIME_DIR (a tmpfs path,
// confirmed present in a non-interactive SSH session via pam_systemd
// -- the same directory moshi-hook's own socket already lives in),
// streams the key there over SSH stdin (never a literal argument
// anywhere, never touching disk in plaintext on either end), runs
// tailscale up against it, and zeroes the local copy immediately
// after the remote write completes -- regardless of whether the
// subsequent tailscale up call succeeds.
//
// The runtime dir is resolved via a real remote round-trip rather
// than assumed or interpolated unresolved into a later command: the
// resulting path is a concrete literal, so it can be safely
// shell-quoted like any other WriteSecretFile/Run argument instead of
// needing an unquoted special case for shell-expansion syntax.
//
// Every remote command in this function runs inside a login shell
// (bash -lc) for the same PATH reason tmuxArgs/reconcile.go's
// innerCmd/cmd construction does: tailscale lives in the instance
// user's home-manager profile, not a system path, and a
// non-interactive SSH command otherwise runs a non-login shell that
// never sources the profile scripts putting ~/.nix-profile/bin on
// PATH.
func JoinTailscale(ctx context.Context, ip, user string) error {
	path, err := secrets.Path()
	if err != nil {
		return err
	}
	key, err := secrets.Decrypt(ctx, path, "tailscale_authkey")
	if err != nil {
		return fmt.Errorf("decrypting tailscale_authkey: %w", err)
	}
	defer secrets.Zero(key)

	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	probeCmd := "bash -lc " + reconcile.ShellQuote(`printf '%s' "$XDG_RUNTIME_DIR"`)
	runtimeDir, err := client.Run(probeCmd)
	if err != nil {
		return fmt.Errorf("resolving instance's runtime directory: %w", err)
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		return fmt.Errorf("instance has no $XDG_RUNTIME_DIR (no active login session for %s?)", user)
	}
	authKeyPath := runtimeDir + "/cloudlab-ts-authkey"

	writeErr := client.WriteSecretFile(authKeyPath, key)
	secrets.Zero(key)
	if writeErr != nil {
		return fmt.Errorf("writing auth key to instance: %w", writeErr)
	}

	// sudo: tailscaled runs as root (see common.nix), and its LocalAPI
	// gates state-changing client calls -- tailscale up included -- on
	// root-or-operator. cloud-init grants the instance user passwordless
	// sudo for exactly this kind of case.
	innerCmd := fmt.Sprintf(`trap 'rm -f %s' EXIT; sudo tailscale up --auth-key=file:%s`,
		reconcile.ShellQuote(authKeyPath), reconcile.ShellQuote(authKeyPath))
	script := "bash -lc " + reconcile.ShellQuote(innerCmd)
	if out, err := client.Run(script); err != nil {
		return fmt.Errorf("tailscale up failed: %w\n%s", err, out)
	}
	return nil
}
