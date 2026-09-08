package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/secrets"
)

// resolveToken returns the DigitalOcean API token, preferring
// DIGITALOCEAN_TOKEN and falling back to digitalocean_token in the
// personal sops secrets file.
//
// The environment wins deliberately, and the reasoning is the opposite
// of the usual instinct that a file beats an env var. Decryption here
// goes through age, commonly an age-plugin-yubikey identity whose touch
// policy can require physical presence. cloudlab exists so agents can
// work unattended, and an agent cannot touch a key -- so the env var is
// the override that keeps unattended provisioning possible, and the
// secrets file is the durable home for an interactive machine. Reading
// the file first would make every up/down block on a key that is not
// there, which is the one outcome this ordering exists to prevent.
//
// Only up, down and status ever need this. Every other command works
// off state.Record and SSH, so an interactive day costs at most one
// decryption on up and one on down.
func resolveToken(ctx context.Context) (string, error) {
	if token := os.Getenv("DIGITALOCEAN_TOKEN"); token != "" {
		return token, nil
	}

	path, err := secrets.Path()
	if err != nil {
		return "", fmt.Errorf("no DigitalOcean token: DIGITALOCEAN_TOKEN is unset, and the secrets file holding digitalocean_token could not be located: %w", err)
	}
	// Decrypting can block silently on a touch prompt (sops shells out
	// to the age plugin, which waits with no output of its own) --
	// reported here specifically, not at the top of the function, so a
	// caller watching for "→ ..." lines knows to look at their key right
	// when the wait actually starts. Same reasoning as JoinTailscale's.
	provider.ReportProgress(ctx, "decrypting digitalocean_token (check your key if it prompts)")
	value, err := secrets.Decrypt(ctx, path, "digitalocean_token")
	if err != nil {
		return "", fmt.Errorf("no DigitalOcean token: DIGITALOCEAN_TOKEN is unset, and reading digitalocean_token from %s failed: %w", path, err)
	}
	defer secrets.Zero(value)

	// sops emits the value with a trailing newline. A token handed to
	// the API with one appended fails authentication in a way that reads
	// like a revoked token rather than a formatting mistake.
	//
	// TrimSpace's copy into a string cannot be scrubbed the way the byte
	// slice can -- an immutable Go string has no memory Zero can reach.
	// The same admission internal/reconcile's placeDoltCredential makes
	// about credsID, and the same one secrets.Zero's own doc comment
	// makes about cmd.Output()'s internal buffering.
	return strings.TrimSpace(string(value)), nil
}
