package sshkeys

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// FileRef keeps the declaring config and original entry for diagnostics,
// even after a relative path has been made absolute.
type FileRef struct {
	Entry      string
	ConfigPath string
	Path       string
}

var (
	numericReference = regexp.MustCompile(`^[0-9]+$`)
	md5Reference     = regexp.MustCompile(`(?i)^(MD5:)?([0-9a-f]{2}:){15}[0-9a-f]{2}$`)
)

// UnsupportedReference recognizes removed account identifiers without
// misclassifying explicit filesystem paths with identifier-like basenames.
func UnsupportedReference(entry string) bool {
	if numericReference.MatchString(entry) || md5Reference.MatchString(entry) {
		return true
	}
	fingerprint := strings.TrimPrefix(entry, "SHA256:")
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(fingerprint, "="))
	return err == nil && len(decoded) == 32 && (len(fingerprint) == 43 || len(fingerprint) == 44 && strings.HasSuffix(fingerprint, "="))
}

// ReadPublicFile accepts exactly one public key. ParseAuthorizedKey alone
// skips unparseable lines, which would silently accept ambiguous files.
func ReadPublicFile(ref FileRef) (Key, error) {
	fail := func(err error) (Key, error) {
		return Key{}, fmt.Errorf("sshKeys entry %q in %s (resolved to %s): supply a public-key file containing exactly one key, not private material: %w", ref.Entry, ref.ConfigPath, ref.Path, err)
	}
	// #nosec G304 -- ref.Path is the public-key file explicitly selected by the user.
	raw, err := os.ReadFile(ref.Path)
	if err != nil {
		return fail(err)
	}
	var key Key
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key.PublicKey != "" {
			return fail(fmt.Errorf("multiple keys or non-comment content"))
		}
		pub, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return fail(err)
		}
		if len(strings.TrimSpace(string(rest))) != 0 {
			return fail(fmt.Errorf("unexpected trailing content"))
		}
		key = Key{Path: ref.Path, Fingerprint: ssh.FingerprintLegacyMD5(pub), Comment: strings.TrimSpace(comment), Source: Disk, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))}
	}
	if key.PublicKey == "" {
		return fail(fmt.Errorf("no public key found"))
	}
	return key, nil
}
