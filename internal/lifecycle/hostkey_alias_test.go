package lifecycle

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestManagedAliasRejectsChangedHostKey(t *testing.T) {
	ctx, c := transportFixture(t)
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: c.Keys[0].PublicPath, Path: c.Keys[0].PublicPath, ConfigPath: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	c.PublicIP = gitSSHServer(t, key, t.TempDir())
	c.PreferredHost = c.PublicIP
	ctx = sshidentity.WithConnection(ctx, c)
	client, err := reconcile.Connect(ctx, c.PublicIP, c.User)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	// A recreated machine at this same address must not inherit trust in a
	// different server key. The alias is not a separate host-key identity.
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(profiles.SSHDir, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{c.PublicIP}, previous.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- real SSH against the disposable selected-key fixture.
	output, err := exec.Command("ssh", "-F", filepath.Join(profiles.SSHDir, "config"), "-o", "UserKnownHostsFile="+known, "-o", "BatchMode=yes", c.Alias, "true").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("changed server key not rejected: %v: %s", err, output)
	}
}
