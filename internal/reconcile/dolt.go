package reconcile

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/secrets"
)

// validDoltCredsID matches a DoltHub creds id -- the JWK's filename stem --
// which becomes part of a remote path built by plain string concatenation
// (doltDir+"/creds/"+credsID+".jwk"). ShellQuote defeats shell
// metacharacters in that path but does nothing about path traversal:
// "install" still resolves ".." components, so an id containing "/" or ".."
// could write outside the tmpfs credential directory to anywhere the
// instance user can write. Restricting it to a plain token forecloses that
// regardless of what the id is later used for.
//
// The first character must be alphanumeric, not just any of the allowed
// characters: a leading "-" would make the id look like a CLI flag
// ("-rf", "--") to anything that ever reads it as a bare leading argument.
// Nothing here does that today, but the whole point of this validator is
// asserting "this is a safe plain token" for whoever uses credsID next, and
// that assertion should hold regardless of where the string travels.
var validDoltCredsID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// sanitizeDoltCredsID rejects anything dolthub_creds_id could be that isn't
// safe to splice into a remote path: empty, or containing a character
// outside validDoltCredsID. A rejection here means the secrets file itself
// is wrong, so the caller warns and falls back to session mode rather than
// trying to sanitize-and-continue.
func sanitizeDoltCredsID(id string) error {
	if id == "" {
		return fmt.Errorf("dolthub_creds_id is empty")
	}
	if !validDoltCredsID.MatchString(id) {
		return fmt.Errorf("dolthub_creds_id %s is not a valid identifier", strconv.Quote(id))
	}
	return nil
}

// doltPrepareScript builds the remote shell script that prepares
// $HOME/.dolt to point at doltDir, without disturbing anything already
// there that isn't cloudlab's own symlink. It is a plain string builder,
// deliberately factored out of placeDoltCredential so it can be run
// directly against a scratch $HOME in tests -- the real target of this
// logic is the shell, not the Go wrapper around it.
//
// -L is checked before -e so a dangling symlink (target gone, -e false) is
// still recognized as a symlink rather than falling through to the -e
// branch and being reported as "no ~/.dolt at all". A symlink already
// pointing at doltDir is left alone (and re-linked, harmlessly, since
// ln -sfn is idempotent); a symlink pointing anywhere else, live or
// dangling, is foreign and reported via NOTASYMLINK exactly like a real
// directory or a regular file -- none of those are cloudlab's to touch.
func doltPrepareScript(doltDir string) string {
	target := ShellQuote(doltDir)
	guard := "if [ -L \"$HOME/.dolt\" ]; then" +
		" [ \"$(readlink \"$HOME/.dolt\")\" = " + target + " ] || { echo NOTASYMLINK; exit 0; }" +
		"; elif [ -e \"$HOME/.dolt\" ]; then echo NOTASYMLINK; exit 0" +
		"; fi"
	return "set -e" +
		"; mkdir -p " + ShellQuote(doltDir+"/creds") +
		"; chmod 700 " + ShellQuote(doltDir) +
		"; " + guard +
		"; ln -sfn " + target + " \"$HOME/.dolt\""
}

// placeDoltCredential ships the user's DoltHub credential to the instance, so
// beads there can sync against the external remote directly.
//
// Per-instance, not per-session: a DoltHub credential identifies the user's
// account, not a session, so it is placed once by the path both `up` and
// `provision` run, and every session on the instance uses it. That is what
// lets N sessions share one instance with no per-session credential juggling,
// and it makes recovery after a reboot `cloudlab provision`, which is already
// idempotent.
//
// ~/.dolt is a symlink to $XDG_RUNTIME_DIR/cloudlab/dolt, which is tmpfs. bd
// and dolt find the credential where they always look, so nothing has to
// remember an environment variable, and the plaintext still only ever exists
// in tmpfs. DOLT_ROOT_PATH was rejected precisely because it would have to be
// threaded through ssh, tmux, herdr and the agent's own environment, and
// anything that missed it would silently fail to authenticate.
//
// Returns nothing. A missing credential warns and falls back to session mode
// rather than failing the reconcile -- deliberately unlike tailscale = true,
// which hard-fails on a missing key. A tailnet the user asked for and did not
// get is a broken instance; beads sharing has a working fallback that needs
// no credential at all.
func placeDoltCredential(ctx context.Context, client *Client, beadsMode string) {
	if beadsMode != "dolthub" {
		return
	}

	path, err := secrets.Path()
	if err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error()+"; issues will sync over the session remote only")
		return
	}
	provider.ReportProgress(ctx, "decrypting DoltHub credential (check for a YubiKey touch prompt)")
	cred, err := secrets.Decrypt(ctx, path, "dolthub_creds")
	if err != nil {
		provider.ReportWarning(ctx, "beads: no dolthub_creds in "+path+" ("+err.Error()+"); issues will sync over the session remote only")
		return
	}
	defer secrets.Zero(cred)

	// Stored beside the key rather than derived from this machine's
	// ~/.dolt/config_global.json, so the instance's configuration does not
	// depend on hidden local state.
	id, err := secrets.Decrypt(ctx, path, "dolthub_creds_id")
	if err != nil {
		provider.ReportWarning(ctx, "beads: no dolthub_creds_id in "+path+" ("+err.Error()+"); issues will sync over the session remote only")
		return
	}
	defer secrets.Zero(id)
	// id is decrypted into a byte slice specifically so secrets.Zero can
	// scrub it; TrimSpace's copy into this string cannot be scrubbed the
	// same way -- an immutable Go string has no memory Zero can reach. Low
	// risk in practice, since this is the JWK's filename stem rather than
	// the credential material itself, but real, the same way secrets.Zero's
	// own doc comment admits what it cannot reach.
	credsID := strings.TrimSpace(string(id))
	if err := sanitizeDoltCredsID(credsID); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error()+"; issues will sync over the session remote only")
		return
	}

	// Resolved by a real remote round-trip rather than assumed, for the same
	// reason JoinTailscale resolves it that way: the result is a concrete
	// literal that can be shell-quoted like any other argument.
	runtimeDir, err := client.Run("bash -lc " + ShellQuote(`printf '%s' "$XDG_RUNTIME_DIR"`))
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not resolve the instance's runtime directory: "+err.Error()+"; issues will sync over the session remote only")
		return
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		provider.ReportWarning(ctx, "beads: the instance has no $XDG_RUNTIME_DIR; issues will sync over the session remote only")
		return
	}

	doltDir := runtimeDir + "/cloudlab/dolt"
	// Anything already at ~/.dolt that is not cloudlab's own symlink to
	// doltDir -- a real directory, a regular file, or a symlink pointing
	// anywhere else (live or dangling) -- is left completely alone: this
	// neither clobbers something the user set up themselves nor writes an
	// account-wide credential to VM disk. See doltPrepareScript's own
	// comment for how each of those states is told apart.
	out, err := client.Run("bash -lc " + ShellQuote(doltPrepareScript(doltDir)))
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not prepare ~/.dolt on the instance: "+err.Error()+"\n"+out)
		return
	}
	if strings.Contains(out, "NOTASYMLINK") {
		provider.ReportWarning(ctx, "beads: ~/.dolt on the instance already exists and is not cloudlab's symlink — leaving it alone and not placing a credential; issues will sync over the session remote only")
		return
	}

	if err := client.WriteSecretFile(doltDir+"/creds/"+credsID+".jwk", cred); err != nil {
		provider.ReportWarning(ctx, "beads: writing the DoltHub credential: "+err.Error())
		return
	}
	globalConfig := fmt.Sprintf("{\"user.creds\":%q}\n", credsID)
	if err := client.WriteSecretFile(doltDir+"/config_global.json", []byte(globalConfig)); err != nil {
		provider.ReportWarning(ctx, "beads: writing the DoltHub config: "+err.Error())
	}
}
