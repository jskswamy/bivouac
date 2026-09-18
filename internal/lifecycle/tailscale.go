package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/secrets"
	"github.com/jskswamy/bivouac/internal/shellcmd"
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
// Every remote command here goes through shellcmd.LoginShell, whose doc
// comment carries the PATH rule: tailscale lives in the instance user's
// home-manager profile, not a system path.
// RemoteTailscaleBin returns the absolute path to tailscale on the
// instance, resolved by a real remote round-trip for the same reason
// JoinTailscale resolves $XDG_RUNTIME_DIR that way.
//
// The absolute path matters because every tailscale call here runs under
// sudo (tailscaled's LocalAPI gates state-changing calls on
// root-or-operator). sudo replaces PATH with its own secure_path --
// system directories only -- and tailscale lives in the instance user's
// home-manager profile, so a bare `sudo tailscale` fails with "command
// not found" however the surrounding shell is invoked. Wrapping in
// `bash -lc` fixes PATH for the shell, not for what sudo then execs.
func RemoteTailscaleBin(ctx context.Context, client *reconcile.Client) (string, error) {
	out, err := client.RunContext(ctx, shellcmd.LoginShell("command -v tailscale"))
	if err != nil {
		return "", fmt.Errorf("tailscale is not installed on the instance — set \"tailscale = true\" in bivouac.pkl and run \"bivouac provision\" first:\n%s", out)
	}
	path := strings.TrimSpace(out)
	if path == "" {
		return "", fmt.Errorf("tailscale is not installed on the instance — set \"tailscale = true\" in bivouac.pkl and run \"bivouac provision\" first")
	}
	return path, nil
}

// TailscaleIP returns the instance's own Tailscale IPv4 address (its
// 100.x.y.z tailnet address), connecting over ip as user. Returns an
// empty string, and no error, when the instance is reachable but has no
// tailnet address -- tailscale installed but logged out, or the daemon
// not running. Only a genuine connection or lookup failure is an error,
// so callers offering the tailnet address as an option can treat "not
// available" as an ordinary outcome rather than something to report.
func TailscaleIP(ctx context.Context, ip, user string) (string, error) {
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()

	bin, err := RemoteTailscaleBin(ctx, client)
	if err != nil {
		return "", nil
	}
	// `tailscale ip -4` prints nothing but an error when the daemon is
	// down or logged out, which is exactly the empty-string case.
	inner := "sudo " + shellcmd.Quote(bin) + " ip -4"
	out, err := client.RunContext(ctx, shellcmd.LoginShell(inner))
	if err != nil {
		return "", nil
	}
	// A node can hold several addresses; the first line is its own IPv4.
	addr := strings.TrimSpace(out)
	if i := strings.IndexByte(addr, '\n'); i >= 0 {
		addr = strings.TrimSpace(addr[:i])
	}
	if !strings.HasPrefix(addr, "100.") {
		return "", nil
	}
	return addr, nil
}

func JoinTailscale(ctx context.Context, ip, user string) error {
	path, err := secrets.Path()
	if err != nil {
		return err
	}
	// Decrypting can block silently on a YubiKey touch prompt (sops
	// shells out to age-plugin-yubikey, which waits for a physical
	// touch with no output of its own) -- report progress before this
	// call specifically, not just at the top of the function, so a
	// caller watching for "→ ..." lines knows to look at their key
	// right when the wait actually starts.
	key, err := secrets.Decrypt(ctx, path, "tailscale_authkey")
	if err != nil {
		return fmt.Errorf("decrypting tailscale_authkey: %w", err)
	}
	defer secrets.Zero(key)

	provider.ReportProgress(ctx, "connecting to instance")
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	probeCmd := shellcmd.LoginShell(`printf '%s' "$XDG_RUNTIME_DIR"`)
	runtimeDir, err := client.Run(probeCmd)
	if err != nil {
		return fmt.Errorf("resolving instance's runtime directory: %w", err)
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		return fmt.Errorf("instance has no $XDG_RUNTIME_DIR (no active login session for %s?)", user)
	}
	authKeyPath := runtimeDir + "/bivouac-ts-authkey"

	// Resolved before the key is shipped, so an instance without
	// tailscale installed fails without a secret having been written to
	// it first.
	tailscaleBin, err := RemoteTailscaleBin(ctx, client)
	if err != nil {
		return err
	}

	provider.ReportProgress(ctx, "writing auth key to instance")
	writeErr := client.WriteSecretFile(authKeyPath, key)
	secrets.Zero(key)
	if writeErr != nil {
		return fmt.Errorf("writing auth key to instance: %w", writeErr)
	}

	provider.ReportProgress(ctx, "joining tailnet")
	// sudo: tailscaled runs as root (see common.nix), and its LocalAPI
	// gates state-changing client calls -- tailscale up included -- on
	// root-or-operator. cloud-init grants the instance user passwordless
	// sudo for exactly this kind of case.
	innerCmd := fmt.Sprintf(`trap 'rm -f %s' EXIT; sudo %s up --auth-key=file:%s`,
		shellcmd.Quote(authKeyPath), shellcmd.Quote(tailscaleBin), shellcmd.Quote(authKeyPath))
	script := shellcmd.LoginShell(innerCmd)
	out, err := client.Run(script)
	if err != nil {
		// tailscaled is a systemd --user unit that common.nix only
		// declares when bivouac.tailscale is true, so the most likely
		// cause is an instance provisioned with the flag off -- the
		// daemon's own message suggests `systemctl start tailscaled`,
		// which does not exist here and sends people the wrong way.
		if strings.Contains(out, "failed to connect to local tailscaled") {
			return fmt.Errorf("tailscaled is not running on the instance — set \"tailscale = true\" in bivouac.pkl and run \"bivouac provision\" to install it:\n%s", out)
		}
		return fmt.Errorf("tailscale up failed: %w\n%s", err, out)
	}
	return nil
}
