package sshkeys

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestReadPublicFile_StrictInput(t *testing.T) {
	priv, pub := newKey(t)
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	block, err := ssh.MarshalPrivateKey(priv, "private fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", "# exported public key\n\n" + canonical + " owner@example\n# trailing comment\n", true},
		{"private", string(pem.EncodeToMemory(block)), false},
		{"multiple", canonical + "\n" + canonical + "\n", false},
		{"empty", "", false},
		{"comments", "# no key\n", false},
		{"malformed", "not a public key\n", false},
		{"leading junk", "junk\n" + canonical + "\n", false},
		{"trailing junk", canonical + "\njunk\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "custom key.pub")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			ref := FileRef{Entry: "./custom key.pub", ConfigPath: filepath.Join(dir, "base.pkl"), Path: path}
			key, err := ReadPublicFile(ref)
			if !tc.valid {
				if err == nil {
					t.Fatal("expected invalid public file error")
				}
				for _, want := range []string{ref.Entry, ref.ConfigPath, "public-key file"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q lacks %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key.Path != path || key.PublicKey != canonical || key.Comment != "owner@example" || key.Fingerprint != fingerprintOf(pub) || key.Source != Disk {
				t.Fatalf("key = %+v", key)
			}
		})
	}
	ref := FileRef{Entry: "missing.pub", ConfigPath: "base.pkl", Path: filepath.Join(t.TempDir(), "missing.pub")}
	if _, err := ReadPublicFile(ref); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestUnsupportedReference(t *testing.T) {
	for _, entry := range []string{"12345", "MD5:10:3e:47:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66", "10:3e:47:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="} {
		if !UnsupportedReference(entry) {
			t.Errorf("legacy reference %q accepted", entry)
		}
	}
	for _, entry := range []string{"./12345", "/keys/12345", "~/keys/12345", "keys/key with spaces.pub", "key.pub", "./AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "./10:3e:47:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66"} {
		if UnsupportedReference(entry) {
			t.Errorf("path %q rejected", entry)
		}
	}
}
