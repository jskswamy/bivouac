package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jskswamy/bivouac/internal/sshkeys"
)

func resolveSSHKeyFiles(configPath string, entries []string) ([]sshkeys.FileRef, error) {
	origin, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolving sshKeys config path %s: %w", configPath, err)
	}
	var refs []sshkeys.FileRef
	var errs []error
	for _, entry := range entries {
		if sshkeys.UnsupportedReference(entry) {
			errs = append(errs, fmt.Errorf("sshKeys entry %q in %s is an unsupported fingerprint or numeric account key ID. sshKeys now accepts public-key file paths only. Replace it with the matching public-key file, for example: sshKeys { \"~/.ssh/id_ed25519.pub\" }. Or run `bivouac init` to select a public-key file", entry, origin))
			continue
		}
		path := entry
		if path == "" {
			errs = append(errs, fmt.Errorf("sshKeys entry %q in %s: supply a public-key file path, for example ~/.ssh/id_ed25519.pub", entry, origin))
			continue
		}
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				errs = append(errs, fmt.Errorf("sshKeys entry %q in %s: resolving home directory: %w", entry, origin, err))
				continue
			}
			path = filepath.Join(home, path[2:])
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(origin), path)
		}
		refs = append(refs, sshkeys.FileRef{Entry: entry, ConfigPath: origin, Path: filepath.Clean(path)})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return refs, nil
}

func configSSHKeyFiles(path string, c Config) ([]sshkeys.FileRef, error) {
	if c.SshKeys == nil {
		return nil, nil
	}
	return resolveSSHKeyFiles(path, *c.SshKeys)
}
