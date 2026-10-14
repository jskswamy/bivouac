package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/wizard"
)

const registerKeysURL = "https://cloud.digitalocean.com/account/security"

type keySetupPolicy int

const (
	keySetupStandalone keySetupPolicy = iota
	keySetupForUp
)

type keySources struct {
	local      func() ([]sshkeys.Key, error)
	registry   func(context.Context) (provider.KeyRegistry, error)
	configPath string
}

// Setup only uses an already available environment token; decrypting sops
// here could silently wait for hardware while displaying selection choices.
func defaultKeySources() keySources {
	return keySources{local: sshkeys.Discover, registry: func(context.Context) (provider.KeyRegistry, error) {
		if token := os.Getenv("DIGITALOCEAN_TOKEN"); token != "" {
			return digitalocean.New(token), nil
		}
		return nil, nil
	}}
}

func askSSHKeys(ctx context.Context, out io.Writer, p prompter, sources keySources, current []string, policy keySetupPolicy) ([]string, error) {
	local, err := sources.local()
	if err != nil {
		return nil, err
	}
	origin := sources.configPath
	if origin == "" {
		origin, err = config.DefaultBasePath()
		if err != nil {
			return nil, err
		}
	}
	var currentPaths []string
	for _, entry := range current {
		if sshkeys.UnsupportedReference(entry) {
			printf(out, "Replace legacy sshKeys entry %q in %s with a public-key file path.\n", entry, origin)
			continue
		}
		refs, err := config.ResolveSSHKeyFiles(origin, []string{entry})
		if err != nil {
			return nil, err
		}
		key, err := sshkeys.ReadPublicFile(refs[0])
		if err != nil {
			return nil, err
		}
		for _, known := range local {
			if known.Fingerprint == key.Fingerprint && known.Source == sshkeys.Agent {
				key.Source = sshkeys.Agent
			}
		}
		local = append(local, key)
		currentPaths = append(currentPaths, key.Path)
	}
	for _, key := range local {
		if key.Path == "" {
			printf(out, "Agent key %s has no public-key file. Export it with ssh-add -L to a .pub file and select that file.\n", key.Comment)
		}
	}
	registry, account, known := lookUpAccount(ctx, out, sources)
	offer := wizard.OfferKeys(local, account, known, currentPaths)
	if len(offer.Choices) == 0 {
		printf(out, "No public-key files discovered. Select a public-key file path, for example ~/.ssh/id_ed25519.pub.\n")
	}
	chosen, err := p.AskKeys(offer)
	if err != nil {
		return nil, err
	}
	refs, err := config.ResolveSSHKeyFiles(origin, chosen)
	if err != nil {
		return nil, err
	}
	var selected []sshkeys.Key
	paths := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		key, err := sshkeys.ReadPublicFile(ref)
		if err != nil {
			return nil, err
		}
		if seen[key.Path] {
			continue
		}
		seen[key.Path] = true
		selected = append(selected, key)
		paths = append(paths, key.Path)
	}
	if len(paths) == 0 {
		printf(out, "Warning: no SSH keys selected. An instance with none will run, bill, and refuse to let you log in.\n")
		return paths, nil
	}
	signingCtx := sshidentity.WithProgress(ctx, func(status string) { printf(out, "%s\n", status) })
	if _, err := sshidentity.Select(signingCtx, selected); err != nil {
		return nil, err
	}
	// Build a fresh offer from validated files so manually entered paths obey
	// exactly the same account matching and registration policy as discovered ones.
	offer = wizard.OfferKeys(selected, account, known, paths)
	if !known {
		printf(out, "Account verification is deferred until `bivouac up`.\n")
		return paths, nil
	}
	for _, key := range wizard.NeedsUpload(offer, paths) {
		if policy == keySetupForUp {
			printf(out, "%s: registration is deferred to up's final confirmation.\n", key.Value)
			continue
		}
		approved, err := p.Confirm(meta{Key: promptUploadKey, Title: fmt.Sprintf("Register public key %s as %s?", key.Value, key.SuggestedName), Description: "Only public material is uploaded; declining keeps the local selection for up."}, false)
		if err != nil {
			return nil, err
		}
		if !approved {
			printf(out, "Registration of %s is deferred until up.\n", key.Value)
			continue
		}
		if err := registerKey(ctx, registry, key.SuggestedName, key); err != nil {
			printf(out, "%v\n", err)
			continue
		}
		printf(out, "Registered %s with your provider account.\n", key.Value)
	}
	return paths, nil
}

func lookUpAccount(ctx context.Context, out io.Writer, sources keySources) (provider.KeyRegistry, []provider.SSHKey, bool) {
	registry, err := sources.registry(ctx)
	if err != nil || registry == nil {
		if err != nil {
			printf(out, "Could not reach your provider account (%v); verification is deferred until up.\n", err)
		}
		return nil, nil, false
	}
	account, err := registry.ListKeys(ctx)
	if err != nil {
		printf(out, "Could not read account SSH keys (%v); account verification is deferred until up.\n", err)
		return registry, nil, false
	}
	return registry, account, true
}

func registerKey(ctx context.Context, registry provider.KeyRegistry, name string, key wizard.KeyChoice) error {
	_, err := registry.CreateKey(ctx, name, key.PublicKey)
	if err == nil || registered(ctx, registry, key.Fingerprint) {
		return nil
	}
	return fmt.Errorf("could not register %s: %w; import public file %q manually at %s or use a token with SSH-key create permission. Selection kept; up will retry after confirmation", key.Value, err, key.Value, registerKeysURL)
}
func registered(ctx context.Context, registry provider.KeyRegistry, fingerprint string) bool {
	keys, err := registry.ListKeys(ctx)
	if err != nil {
		return false
	}
	for _, key := range keys {
		if key.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}
func currentKeys(values config.Values) []string {
	keys, _ := values[config.FieldSSHKeys].([]string)
	return keys
}

// Migration runs before normal Resolve, including when personal settings are
// reused or the user declines moving project keys into the personal file.
func repairLegacyKeys(ctx context.Context, out io.Writer, p prompter, path string, values config.Values, keys keySources, policy keySetupPolicy) (config.Values, error) {
	legacy := false
	for _, entry := range currentKeys(values) {
		legacy = legacy || sshkeys.UnsupportedReference(entry)
	}
	if !legacy {
		return values, nil
	}
	keys.configPath = path
	chosen, err := askSSHKeys(ctx, out, p, keys, currentKeys(values), policy)
	if err != nil {
		return nil, err
	}
	values = merge(config.Values{}, values)
	if len(chosen) > 0 {
		values[config.FieldSSHKeys] = chosen
	} else {
		delete(values, config.FieldSSHKeys)
	}
	return values, nil
}
