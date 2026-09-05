package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// generateTestAgeRecipient creates a fresh age identity, points
// SOPS_AGE_KEY_FILE at it for the test's duration, and returns its
// public key. Duplicated from internal/secrets's setupSecretsTest and
// internal/lifecycle's writeTailscaleSecretsFixture rather than
// extracted into a shared test-support package -- same call already
// made for startFakeAgent/startFakeSSHServer (see ready_test.go):
// each package is only the Nth consumer, not worth it yet.
func generateTestAgeRecipient(t *testing.T) string {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "age-key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v\n%s", err, out)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	var recipient string
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

func TestSecretsInit_CreatesEncryptedFile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "init", "--age", recipient})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets init error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "cloudlab", "secrets.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("secrets file not created: %v", err)
	}
}

func TestSecretsInit_FailsWithoutAgeFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "init"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err == nil {
		t.Fatal("expected error with no --age recipient")
	}
}

func TestSecretsKeys_ReportsNoKeysForFreshFile(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "keys"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("secrets keys error = %v", err)
	}
	if !strings.Contains(out.String(), "no keys") {
		t.Errorf("output = %q, want it to report no keys for a freshly-initialized file", out.String())
	}
}
