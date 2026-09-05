package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/secrets"
)

// writeTailscaleSecretsFixture generates a fresh age identity, points
// SOPS_AGE_KEY_FILE and XDG_CONFIG_HOME at temp locations, and writes
// a real sops-encrypted secrets.yaml containing tailscale_authkey --
// exactly what JoinTailscale decrypts in production. The age-keygen
// boilerplate is duplicated from internal/secrets's own
// setupSecretsTest (and cmd/secrets_test.go's generateTestAgeRecipient)
// rather than extracted into a shared test-support package -- same
// call this codebase already made for startFakeAgent/startFakeSSHServer
// (see ready_test.go's doc comment): each is only the Nth consumer, not
// worth it yet.
func writeTailscaleSecretsFixture(t *testing.T, authkey string) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "age-key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v\n%s", err, out)
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	var recipient string
	for _, line := range strings.Split(string(keyData), "\n") {
		if strings.HasPrefix(line, "# public key: ") {
			recipient = strings.TrimPrefix(line, "# public key: ")
		}
	}
	if recipient == "" {
		t.Fatalf("couldn't find public key in age-keygen output:\n%s", keyData)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyPath)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	plainPath := filepath.Join(t.TempDir(), "plain.yaml")
	if err := os.WriteFile(plainPath, []byte("tailscale_authkey: "+authkey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sops", "--age", recipient, "-e", plainPath).CombinedOutput()
	if err != nil {
		t.Fatalf("sops -e: %v\n%s", err, out)
	}
	secretsPath, err := secrets.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(secretsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJoinTailscale_WritesKeyAndRunsTailscaleUp(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())
	writeTailscaleSecretsFixture(t, "tskey-abc123-example")

	var commands []string
	var stdins [][]byte
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		stdins = append(stdins, stdin)
		if strings.Contains(cmd, "printf") {
			return "/run/user/1000", 0
		}
		return "", 0
	})

	if err := JoinTailscale(context.Background(), addr, "devuser"); err != nil {
		t.Fatalf("JoinTailscale() error = %v", err)
	}

	if len(commands) != 3 {
		t.Fatalf("commands = %v, want 3 (resolve runtime dir, write key, tailscale up)", commands)
	}
	if !strings.Contains(commands[0], "XDG_RUNTIME_DIR") {
		t.Errorf("commands[0] = %q, want it to resolve $XDG_RUNTIME_DIR", commands[0])
	}
	if !strings.HasPrefix(commands[0], "bash -lc ") {
		t.Errorf("commands[0] = %q, want it wrapped in a login shell so tailscale's PATH is set", commands[0])
	}
	if !strings.Contains(commands[1], "install -m 600") || !strings.Contains(commands[1], "/run/user/1000/cloudlab-ts-authkey") {
		t.Errorf("commands[1] = %q, want an install -m 600 into the resolved runtime dir", commands[1])
	}
	if string(stdins[1]) != "tskey-abc123-example" {
		t.Errorf("stdin to the write step = %q, want the decrypted auth key", stdins[1])
	}
	if !strings.Contains(commands[2], "sudo tailscale up --auth-key=file:") || !strings.Contains(commands[2], "/run/user/1000/cloudlab-ts-authkey") {
		t.Errorf("commands[2] = %q, want sudo tailscale up --auth-key=file:<path>", commands[2])
	}
	if !strings.Contains(commands[2], "trap") {
		t.Errorf("commands[2] = %q, want a trap to clean up the tmpfs file", commands[2])
	}
	if !strings.HasPrefix(commands[2], "bash -lc ") {
		t.Errorf("commands[2] = %q, want it wrapped in a login shell so tailscale's PATH is set", commands[2])
	}
	for i, cmd := range commands {
		if strings.Contains(cmd, "tskey-abc123-example") {
			t.Errorf("commands[%d] = %q contains the secret literal -- it must only travel via stdin", i, cmd)
		}
	}
}

func TestJoinTailscale_MissingSecretsFile_ReturnsClearError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	err := JoinTailscale(context.Background(), "192.0.2.1", "devuser")
	if err == nil {
		t.Fatal("JoinTailscale() error = nil, want error when secrets.yaml doesn't exist")
	}
	if !strings.Contains(err.Error(), "doesn't exist yet") {
		t.Errorf("error = %q, want it to explain the secrets file is missing", err.Error())
	}
}
