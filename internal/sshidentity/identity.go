package sshidentity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

func readIdentity(id Identity) (sshkeys.Key, error) {
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: id.PublicPath, Path: id.PublicPath, ConfigPath: "recorded connection identity"})
	if err != nil {
		return sshkeys.Key{}, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		return sshkeys.Key{}, err
	}
	if ssh.FingerprintSHA256(pub) != id.Fingerprint {
		return sshkeys.Key{}, fmt.Errorf("selected public-key file %s changed identity; restore the recorded key", id.PublicPath)
	}
	return key, nil
}

func privateSigner(id Identity) (ssh.Signer, error) {
	if id.PrivatePath == "" {
		return nil, fmt.Errorf("no matching private file for %s; load its key into an SSH agent with ssh-add", id.PublicPath)
	}
	// #nosec G304 -- the recorded sibling private path is explicitly selected local identity material; it is never uploaded.
	raw, err := os.ReadFile(id.PrivatePath)
	if err != nil {
		return nil, fmt.Errorf("selected public key %s: reading private key %s: %w; load the matching key with ssh-add", id.PublicPath, id.PrivatePath, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("loading selected private key %s: %w; run ssh-add %q and retry", id.PrivatePath, err, id.PrivatePath)
	}
	if ssh.FingerprintSHA256(signer.PublicKey()) != id.Fingerprint {
		return nil, fmt.Errorf("private key %s does not match selected public key %s", id.PrivatePath, id.PublicPath)
	}
	return signer, nil
}

func matchingAgent(signers []ssh.Signer, fingerprint string) ssh.Signer {
	for _, signer := range signers {
		if ssh.FingerprintSHA256(signer.PublicKey()) == fingerprint {
			return signer
		}
	}
	return nil
}

func challenge(ctx context.Context, signer ssh.Signer, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	status := fmt.Sprintf("Checking selected SSH signing capability for %s (hardware keys may require a touch)", path)
	if fn, ok := ctx.Value(progressKey{}).(func(string)); ok && fn != nil {
		fn(status)
	} else {
		_, _ = fmt.Fprintln(os.Stderr, status)
	}
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return err
	}
	sig, err := signer.Sign(rand.Reader, data)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("signing check for %s: %w", path, err)
	}
	if err := signer.PublicKey().Verify(data, sig); err != nil {
		return fmt.Errorf("signature verification for %s: %w", path, err)
	}
	return nil
}

// Select requires at least one actual local signing capability. Other selected
// public files can authorize recipients that do not hold a key on this machine.
func Select(ctx context.Context, keys []sshkeys.Key) ([]Identity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no public-key files selected for local SSH login")
	}
	agentSigners, cleanup, agentErr := connectAgent(ctx)
	defer cleanup()
	if errors.Is(agentErr, context.Canceled) || errors.Is(agentErr, context.DeadlineExceeded) {
		return nil, agentErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var ids []Identity
	var failures []error
	seen := map[string]bool{}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
		if err != nil {
			return nil, fmt.Errorf("selected public key %s: %w", key.Path, err)
		}
		id := Identity{PublicPath: key.Path, Fingerprint: ssh.FingerprintSHA256(pub)}
		if _, err := readIdentity(id); err != nil {
			return nil, err
		}
		if seen[id.Fingerprint] {
			continue
		}
		seen[id.Fingerprint] = true
		var signer ssh.Signer
		if signer = matchingAgent(agentSigners, id.Fingerprint); signer != nil {
			id.Agent = true
		} else {
			if strings.HasSuffix(key.Path, ".pub") {
				id.PrivatePath = strings.TrimSuffix(key.Path, ".pub")
			}
			signer, err = privateSigner(id)
		}
		if err == nil && signer != nil {
			err = challenge(ctx, signer, key.Path)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		if agentErr != nil {
			failures = append(failures, fmt.Errorf("current SSH agent unavailable: %w", agentErr))
		}
		return nil, fmt.Errorf("no selected key is usable for local SSH login; supply a matching private file or load a matching agent key with ssh-add: %w", errors.Join(failures...))
	}
	return ids, nil
}

// Signers revalidates recorded identities and offers only those signers. It
// deliberately never falls back to unrelated keys or changes signing mode.
func Signers(ctx context.Context, identities []Identity) ([]ssh.Signer, func(), error) {
	noop := func() {}
	if len(identities) == 0 {
		return nil, noop, fmt.Errorf("recorded connection has no selected SSH identities")
	}
	if err := ctx.Err(); err != nil {
		return nil, noop, err
	}
	needsAgent := false
	for _, id := range identities {
		if _, err := readIdentity(id); err != nil {
			return nil, noop, err
		}
		needsAgent = needsAgent || id.Agent
	}
	var agentSigners []ssh.Signer
	cleanup := noop
	if needsAgent {
		var err error
		agentSigners, cleanup, err = connectAgent(ctx)
		if err != nil {
			return nil, noop, fmt.Errorf("loading recorded SSH agent identity: %w", err)
		}
	}
	var signers []ssh.Signer
	for _, id := range identities {
		var signer ssh.Signer
		var err error
		if id.Agent {
			signer = matchingAgent(agentSigners, id.Fingerprint)
			if signer == nil {
				err = fmt.Errorf("current SSH agent no longer supplies selected key %s; run ssh-add for its matching key", id.PublicPath)
			}
		} else {
			signer, err = privateSigner(id)
		}
		if err != nil {
			cleanup()
			return nil, noop, err
		}
		signers = append(signers, signer)
	}
	return signers, cleanup, nil
}
