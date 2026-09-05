package secrets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupSecretsTest generates a fresh age identity and points
// SOPS_AGE_KEY_FILE at it for the test's duration, returning its
// public key (recipient) -- so tests exercise the real sops+age
// path, which is this package's entire job.
func setupSecretsTest(t *testing.T) (recipient string) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "age-key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v\n%s", err, out)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "# public key: ") {
			recipient = strings.TrimPrefix(line, "# public key: ")
		}
	}
	if recipient == "" {
		t.Fatalf("couldn't find public key in age-keygen output:\n%s", data)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyPath)
	return recipient
}

// writeEncryptedFixture sops-encrypts plaintextYAML for recipient and
// returns the resulting file's path under dir.
func writeEncryptedFixture(t *testing.T, dir, recipient, plaintextYAML string) string {
	t.Helper()
	plainPath := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plainPath, []byte(plaintextYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sops", "--age", recipient, "-e", plainPath).CombinedOutput()
	if err != nil {
		t.Fatalf("sops -e: %v\n%s", err, out)
	}
	encPath := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(encPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return encPath
}

func TestDecrypt_ReturnsPlaintextValue(t *testing.T) {
	recipient := setupSecretsTest(t)
	path := writeEncryptedFixture(t, t.TempDir(), recipient, "tailscale_authkey: tskey-abc123-example\n")

	got, err := Decrypt(context.Background(), path, "tailscale_authkey")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "tskey-abc123-example" {
		t.Errorf("Decrypt() = %q, want %q", got, "tskey-abc123-example")
	}
}

func TestDecrypt_MissingFile_ReturnsClearError(t *testing.T) {
	_, err := Decrypt(context.Background(), filepath.Join(t.TempDir(), "no-such.yaml"), "tailscale_authkey")
	if err == nil {
		t.Fatal("Decrypt() error = nil, want error for a missing secrets file")
	}
	if !strings.Contains(err.Error(), "doesn't exist yet") {
		t.Errorf("error = %q, want it to explain the file is missing", err.Error())
	}
}

func TestDecrypt_MissingKey_ReturnsSopsError(t *testing.T) {
	recipient := setupSecretsTest(t)
	path := writeEncryptedFixture(t, t.TempDir(), recipient, "other_key: some-value\n")

	_, err := Decrypt(context.Background(), path, "tailscale_authkey")
	if err == nil {
		t.Fatal("Decrypt() error = nil, want error for a key that isn't in the file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to mention the key wasn't found", err.Error())
	}
}

func TestZero_OverwritesBytes(t *testing.T) {
	b := []byte("secret-value")
	Zero(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("b[%d] = %d, want 0", i, v)
		}
	}
}

func TestPath_UsesXDGConfigHomeWhenSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/custom/config", "cloudlab", "secrets.yaml")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPath_FallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "cloudlab", "secrets.yaml")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestKeys_ListsTopLevelKeysExcludingSops(t *testing.T) {
	recipient := setupSecretsTest(t)
	path := writeEncryptedFixture(t, t.TempDir(), recipient, "tailscale_authkey: tskey-abc\nother_key: value\n")

	got, err := Keys(path)
	if err != nil {
		t.Fatalf("Keys() error = %v", err)
	}
	want := []string{"other_key", "tailscale_authkey"}
	if len(got) != len(want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Keys()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestKeys_MissingFile_ReturnsClearError(t *testing.T) {
	_, err := Keys(filepath.Join(t.TempDir(), "no-such.yaml"))
	if err == nil {
		t.Fatal("Keys() error = nil, want error for a missing secrets file")
	}
	if !strings.Contains(err.Error(), "doesn't exist yet") {
		t.Errorf("error = %q, want it to explain the file is missing", err.Error())
	}
}

func TestInit_CreatesEncryptedFileWithNoKeys(t *testing.T) {
	recipient := setupSecretsTest(t)
	path := filepath.Join(t.TempDir(), "secrets.yaml")

	if err := Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	keys, err := Keys(path)
	if err != nil {
		t.Fatalf("Keys() error = %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("Keys() = %v, want empty for a freshly-initialized file", keys)
	}
}

func TestInit_FailsIfFileAlreadyExists(t *testing.T) {
	recipient := setupSecretsTest(t)
	path := filepath.Join(t.TempDir(), "secrets.yaml")
	if err := Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("first Init() error = %v", err)
	}

	err := Init(context.Background(), path, []string{recipient})
	if err == nil {
		t.Fatal("second Init() error = nil, want error since the file already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to say the file already exists", err.Error())
	}
}

func TestInit_RequiresAtLeastOneRecipient(t *testing.T) {
	err := Init(context.Background(), filepath.Join(t.TempDir(), "secrets.yaml"), nil)
	if err == nil {
		t.Fatal("Init() error = nil, want error with no recipients")
	}
}
