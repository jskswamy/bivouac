package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/wizard"
)

// registerKeysURL is where a user registers a key by hand when bivouac
// cannot do it for them.
const registerKeysURL = "https://cloud.digitalocean.com/account/security"

// keySources supplies the SSH key question's two inputs.
//
// Separated from the flow so the tests can drive every tier without an
// ssh-agent, a token, or a network.
type keySources struct {
	// local returns the keys this machine holds.
	local func() ([]sshkeys.Key, error)
	// registry returns the provider's key API, or nil to stay on local
	// keys alone.
	registry func(ctx context.Context) (provider.KeyRegistry, error)
}

// defaultKeySources reads the real machine and, only when a token is
// already in the environment, the real account.
//
// The environment is the whole test. resolveToken's other path decrypts
// through sops, commonly an age-plugin-yubikey identity whose touch
// policy waits for physical presence with no output of its own -- so
// reaching for it here would leave a form sitting silently on a key
// nobody has been asked to touch, which is the same failure as a form
// waiting on stdin that never comes. The first run on a fresh machine
// must complete with no credentials at all.
func defaultKeySources() keySources {
	return keySources{
		local: sshkeys.Discover,
		registry: func(ctx context.Context) (provider.KeyRegistry, error) {
			token := os.Getenv("DIGITALOCEAN_TOKEN")
			if token == "" {
				return nil, nil
			}
			return digitalocean.New(token), nil
		},
	}
}

// askSSHKeys asks which keys the instance should accept, offering what
// this machine holds and -- when a registry is available -- what the
// account already has.
//
// Returns the fingerprints to write. Fingerprints rather than numeric
// IDs: a fingerprint names the key material, so it survives the key
// being removed and re-added, means the same thing in another account,
// and can be checked by hand with `ssh-keygen -lE md5`.
func askSSHKeys(ctx context.Context, out io.Writer, p prompter, sources keySources, current []string) ([]string, error) {
	local, err := sources.local()
	if err != nil {
		return nil, err
	}

	registry, account, accountKnown := lookUpAccount(ctx, out, sources)
	offer := wizard.OfferKeys(local, account, accountKnown, current)

	if len(offer.Choices) == 0 && !accountKnown {
		printf(out, "No SSH keys found on this machine. Create one with `ssh-keygen -t ed25519`, or enter a fingerprint already registered with your provider.\n")
	}

	chosen, err := p.AskKeys(offer)
	if err != nil {
		return nil, err
	}

	chosen, uploaded := uploadMissing(ctx, out, p, registry, offer, chosen)
	warnUnregistered(out, offer, chosen, uploaded)

	if len(chosen) == 0 {
		// The one failure mode nothing downstream catches: an instance
		// with no keys boots, bills, and refuses every login. A bad
		// fingerprint at least fails the create.
		printf(out, "\nWarning: no SSH keys selected. An instance created with none will run, bill, and refuse to let you log in.\n")
	}
	return chosen, nil
}

// lookUpAccount fetches the account's keys, degrading to local-only
// rather than failing.
//
// A token scoped without the SSH-key permission is the configuration
// bivouac recommends, so a refusal here is an answer -- this tier is
// unavailable -- not an error worth stopping for.
func lookUpAccount(ctx context.Context, out io.Writer, sources keySources) (provider.KeyRegistry, []provider.SSHKey, bool) {
	registry, err := sources.registry(ctx)
	if err != nil || registry == nil {
		if err != nil {
			printf(out, "Could not reach your provider account (%v); offering this machine's keys only.\n", err)
		}
		return nil, nil, false
	}

	account, err := registry.ListKeys(ctx)
	if err != nil {
		if provider.IsForbidden(err) {
			printf(out, "This token cannot read your account's SSH keys, so bivouac cannot say which are registered. Offering this machine's keys only.\n")
		} else {
			printf(out, "Could not list your account's SSH keys (%v); offering this machine's keys only.\n", err)
		}
		return registry, nil, false
	}
	return registry, account, true
}

// uploadMissing offers to register each chosen key the account lacks,
// and returns the choice with anything that stayed unregistered removed.
//
// Removed rather than kept, because DigitalOcean refuses to create a
// droplet naming a key it does not have -- one unregistered entry fails
// every later `up` until the file is edited again.
func uploadMissing(ctx context.Context, out io.Writer, p prompter, registry provider.KeyRegistry, offer wizard.KeyOffer, chosen []string) (kept []string, uploaded map[string]bool) {
	uploaded = map[string]bool{}
	missing := wizard.NeedsUpload(offer, chosen)
	if len(missing) == 0 || registry == nil {
		return chosen, uploaded
	}

	dropped := map[string]bool{}
	for _, key := range missing {
		ok, err := p.Confirm(meta{
			Key:         promptUploadKey,
			Title:       fmt.Sprintf("%s is not on your provider account. Upload it?", key.Label),
			Description: "Registers the public key so instances can accept it. Nothing private leaves this machine.",
		}, true)
		if err != nil || !ok {
			printf(out, "Leaving %s out; register it at %s to use it later.\n", key.Label, registerKeysURL)
			dropped[key.Fingerprint] = true
			continue
		}

		name, err := p.Input(meta{
			Key:         promptUploadKeyName,
			Title:       "Name it:",
			Description: "How the key will be listed on your provider account.",
		}, key.SuggestedName)
		if err != nil || name == "" {
			name = key.SuggestedName
		}

		if err := registerKey(ctx, registry, name, key); err != nil {
			printf(out, "%v\n", err)
			dropped[key.Fingerprint] = true
			continue
		}
		uploaded[key.Fingerprint] = true
		printf(out, "Registered %s with your provider account.\n", key.Label)
	}

	kept = make([]string, 0, len(chosen))
	for _, fp := range chosen {
		if !dropped[fp] {
			kept = append(kept, fp)
		}
	}
	return kept, uploaded
}

// warnUnregistered names any chosen key the account still lacks.
//
// These are the ones nothing could upload: a fingerprint typed into the
// free-text entry, or one carried in from an existing config. There is
// no public key for either, so the only thing to do is say so -- and
// saying so matters, because the create this will fail is reported as
// "invalid key identifiers" and has been seen blaming the hostname
// instead.
//
// Named rather than dropped. The user asked for these explicitly, and
// may be about to register them by hand.
func warnUnregistered(out io.Writer, offer wizard.KeyOffer, chosen []string, uploaded map[string]bool) {
	var missing []string
	for _, fp := range offer.Unregistered(chosen) {
		if !uploaded[fp] {
			missing = append(missing, fp)
		}
	}
	if len(missing) == 0 {
		return
	}
	printf(out, "\nWarning: %s not on your provider account. `up` will refuse to create an instance until registered at %s.\n",
		strings.Join(missing, ", "), registerKeysURL)
}

// registerKey uploads one key, treating "it was already there" as
// success.
//
// A create can fail because the key is on the account already -- a
// race, or the same key registered under another name. The question
// worth answering is whether the fingerprint is now present, so that is
// the question asked, rather than matching the provider's error text,
// which is not ours to depend on.
func registerKey(ctx context.Context, registry provider.KeyRegistry, name string, key wizard.KeyChoice) error {
	_, err := registry.CreateKey(ctx, name, key.PublicKey)
	if err == nil {
		return nil
	}
	if registered(ctx, registry, key.Fingerprint) {
		return nil
	}
	if provider.IsForbidden(err) {
		return fmt.Errorf("this token cannot add SSH keys, so %s was left out — register it at %s, or re-run with a token that can", key.Label, registerKeysURL)
	}
	return fmt.Errorf("could not register %s (%v), so it was left out — register it at %s", key.Label, err, registerKeysURL)
}

func registered(ctx context.Context, registry provider.KeyRegistry, fingerprint string) bool {
	account, err := registry.ListKeys(ctx)
	if err != nil {
		return false
	}
	for _, k := range account {
		if k.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}

// currentKeys reads the sshKeys already in a config's values.
func currentKeys(values config.Values) []string {
	keys, _ := values[config.FieldSSHKeys].([]string)
	return keys
}
