package testenv

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

// SSHKey creates a disposable custom public/private pair for local tests.
// It deliberately does not touch the user's agent or default identity files.
func SSHKey(t *testing.T) sshkeys.Key {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom identity.pub")
	if err := os.WriteFile(path, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".pub"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: path, Path: path, ConfigPath: "test fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return key
}
