// Package keyplan separates read-only SSH account planning from approved writes.
package keyplan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
)

type Entry struct {
	Key         sshkeys.Key
	Registered  bool
	AccountName string
}
type Plan struct {
	Entries    []Entry
	Identities []sshidentity.Identity
}

func readSelected(key sshkeys.Key) (sshkeys.Key, error) {
	current, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: key.Path, Path: key.Path, ConfigPath: "approved SSH key selection"})
	if err != nil {
		return sshkeys.Key{}, err
	}
	if current.PublicKey != key.PublicKey || current.Fingerprint != key.Fingerprint {
		return sshkeys.Key{}, fmt.Errorf("selected public-key file %s changed after selection; review a new plan before proceeding", key.Path)
	}
	return current, nil
}
func list(ctx context.Context, registry provider.KeyRegistry) ([]provider.SSHKey, error) {
	if registry == nil {
		return nil, fmt.Errorf("provider does not support SSH-key account listing")
	}
	keys, err := registry.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot read account SSH keys; grant SSH-key read permission and retry: %w", err)
	}
	return keys, nil
}
func accountMap(keys []provider.SSHKey) map[string]provider.SSHKey {
	account := map[string]provider.SSHKey{}
	for _, key := range keys {
		account[key.Fingerprint] = key
	}
	return account
}

func Build(ctx context.Context, registry provider.KeyRegistry, keys []sshkeys.Key) (Plan, error) {
	var selected []sshkeys.Key
	seen := map[string]bool{}
	for _, key := range keys {
		current, err := readSelected(key)
		if err != nil {
			return Plan{}, err
		}
		if !seen[current.Fingerprint] {
			selected = append(selected, current)
			seen[current.Fingerprint] = true
		}
	}
	identities, err := sshidentity.Select(ctx, selected)
	if err != nil {
		return Plan{}, err
	}
	accountKeys, err := list(ctx, registry)
	if err != nil {
		return Plan{}, err
	}
	account := accountMap(accountKeys)
	plan := Plan{Identities: identities}
	for _, key := range selected {
		registered, ok := account[key.Fingerprint]
		name := "bivouac-" + strings.TrimSuffix(filepath.Base(key.Path), ".pub")
		if ok {
			name = registered.Name
		}
		plan.Entries = append(plan.Entries, Entry{Key: key, Registered: ok, AccountName: name})
	}
	return plan, nil
}

// RegisterPublic shares explicitly approved standalone setup registration with
// creation execution. It revalidates public bytes and accepts a create race
// only when an authoritative re-list finds the same identity.
func RegisterPublic(ctx context.Context, registry provider.KeyRegistry, selected sshkeys.Key, name string) (provider.SSHKey, error) {
	if _, err := readSelected(selected); err != nil {
		return provider.SSHKey{}, err
	}
	if registry == nil || !strings.HasPrefix(name, "bivouac-") {
		return provider.SSHKey{}, fmt.Errorf("unapproved SSH registration")
	}
	key, err := registry.CreateKey(ctx, name, selected.PublicKey)
	if err != nil {
		if raced, listErr := list(ctx, registry); listErr == nil {
			if existing, ok := accountMap(raced)[selected.Fingerprint]; ok {
				return existing, nil
			}
		}
		return provider.SSHKey{}, fmt.Errorf("cannot register public key %s as %q: %w; grant SSH-key create permission or import manually: doctl compute ssh-key import %s --public-key-file %s (https://cloud.digitalocean.com/account/security)", selected.Path, name, err, shellcmd.Quote(name), shellcmd.Quote(selected.Path))
	}
	if key.Fingerprint != selected.Fingerprint {
		return provider.SSHKey{}, fmt.Errorf("provider returned a different identity for %s; stop and inspect the account", selected.Path)
	}
	return key, nil
}

// Register only adds approved public keys. Re-listing first makes retries
// idempotent even after partial registration or a competing creator.
func Register(ctx context.Context, registry provider.KeyRegistry, plan Plan) ([]string, error) {
	for _, entry := range plan.Entries {
		if _, err := readSelected(entry.Key); err != nil {
			return nil, err
		}
	}
	if err := sshidentity.CheckSigners(ctx, plan.Identities); err != nil {
		return nil, err
	}
	keys, err := list(ctx, registry)
	if err != nil {
		return nil, err
	}
	account := accountMap(keys)
	var ids []string
	additions := 0
	for _, entry := range plan.Entries {
		key, ok := account[entry.Key.Fingerprint]
		if !ok {
			if entry.Registered {
				return nil, fmt.Errorf("approved registered key %s is no longer on the account; review a new plan before creating the instance", entry.Key.Path)
			}
			if !strings.HasPrefix(entry.AccountName, "bivouac-") {
				return nil, fmt.Errorf("unapproved account key name %q", entry.AccountName)
			}
			key, err = RegisterPublic(ctx, registry, entry.Key, entry.AccountName)
			if err != nil {
				return nil, fmt.Errorf("%d earlier account additions are retained; retry is idempotent: %w", additions, err)
			}
			additions++
			account[entry.Key.Fingerprint] = key
		}
		id := key.ID
		if id == "" {
			id = key.Fingerprint
		}
		ids = append(ids, id)
	}
	return ids, nil
}
