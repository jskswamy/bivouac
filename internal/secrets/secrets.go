// Package secrets manages cloudlab's personal, sops-encrypted secrets
// file (Tailscale auth keys today, whatever else needs one later):
// decrypting individual values just-in-time, listing key names, and
// creating the file for a fresh setup. Every operation shells out to
// sops -- it is never linked as a Go library, matching this
// codebase's existing convention for tailscale/mutagen/nix.
package secrets

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Path returns the location of cloudlab's personal secrets file:
// $XDG_CONFIG_HOME/cloudlab/secrets.yaml if set, else
// ~/.config/cloudlab/secrets.yaml -- same XDG convention as
// internal/config's resolveBasePath.
func Path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cloudlab", "secrets.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cloudlab", "secrets.yaml"), nil
}

// Decrypt returns the decrypted value of key in the sops-encrypted
// YAML file at path, as a byte slice the caller must Zero immediately
// after its one use. Fails with a specific error if path doesn't
// exist yet (checked before ever invoking sops); a missing key
// surfaces sops's own "component [...] not found" message.
func Decrypt(ctx context.Context, path, key string) ([]byte, error) {
	if _, err := exec.LookPath("sops"); err != nil {
		return nil, fmt.Errorf("sops not found on PATH: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("secrets file %s doesn't exist yet (run `cloudlab secrets init`)", path)
		}
		return nil, err
	}
	// #nosec G204 -- argv-array exec.Command, no shell; path is
	// cloudlab's own fixed secrets path and key is a fixed constant
	// supplied by callers in this codebase, never external input.
	cmd := exec.CommandContext(ctx, "sops", "-d", "--extract", fmt.Sprintf(`["%s"]`, key), path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops -d --extract %q %s: %w\n%s", key, path, err, stderr.String())
	}
	return out, nil
}

// Zero overwrites b's contents with zero bytes in place, so a
// decrypted secret doesn't linger in memory past its one use. This
// only guarantees the caller's own slice is cleared: Decrypt's data
// arrives via cmd.Output(), which copies through internal buffering
// (bytes.Buffer, an io.Copy buffer) that Zero has no way to reach, so
// other copies of the secret may still exist in memory afterward.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// topLevelKeyPattern matches a top-level "key:" line in a
// sops-encrypted YAML file. sops only encrypts values, not keys, so
// Keys reads the file directly with no decryption at all. Deliberately
// a simple line scan, not a YAML parser: this file is meant to stay a
// flat list of secrets, never nested mappings, so pulling in a YAML
// dependency for one command isn't worth it.
var topLevelKeyPattern = regexp.MustCompile(`^([A-Za-z0-9_]+):`)

// Keys returns the top-level key names in the secrets file at path,
// excluding sops's own "sops" metadata key, sorted alphabetically.
func Keys(path string) ([]string, error) {
	// #nosec G304 -- path is always secrets.Path()'s fixed location
	// (or a caller-trusted equivalent in tests), never external input.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("secrets file %s doesn't exist yet (run `cloudlab secrets init`)", path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var keys []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := topLevelKeyPattern.FindStringSubmatch(scanner.Text())
		if m == nil || m[1] == "sops" {
			continue
		}
		keys = append(keys, m[1])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// Init creates path as a fresh sops-encrypted secrets file with no
// keys yet, encrypted for recipients (age public keys or
// age-plugin-yubikey identities, comma-joined into sops's own --age
// flag). Fails if path already exists -- use Edit to change one.
func Init(ctx context.Context, path string, recipients []string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("at least one --age recipient is required")
	}
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops not found on PATH: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists (use `cloudlab secrets edit` to change it)", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// #nosec G204 -- argv-array exec.Command, no shell; recipients are
	// age public keys supplied via cloudlab's own --age flag, passed
	// through to sops's identical flag, never shell-interpreted.
	cmd := exec.CommandContext(ctx, "sops",
		"--age", strings.Join(recipients, ","),
		"--input-type", "yaml", "--output-type", "yaml",
		"-e", "/dev/stdin")
	cmd.Stdin = strings.NewReader("{}\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sops -e: %w\n%s", err, stderr.String())
	}
	return os.WriteFile(path, out, 0o600)
}

// Edit opens path in $EDITOR via sops's own edit flow (decrypt,
// $EDITOR, re-encrypt), execing the real sops binary with stdio
// passed straight through -- same thin-client shape as
// lifecycle.SSH/Herdr/Tmux, just not instance-scoped.
func Edit(ctx context.Context, path string) error {
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops not found on PATH: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("secrets file %s doesn't exist yet (run `cloudlab secrets init`)", path)
		}
		return err
	}
	// #nosec G204 -- argv-array exec.Command, no shell; path is
	// cloudlab's own fixed secrets path, never external input.
	cmd := exec.CommandContext(ctx, "sops", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
