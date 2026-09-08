package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/secrets"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// writeTokenSecretsFixture points XDG_CONFIG_HOME at a temp directory
// and writes a real sops-encrypted secrets.yaml there holding
// digitalocean_token, so resolveToken's fallback runs against the same
// sops+age path production uses rather than a stub.
//
// Pinning XDG_CONFIG_HOME is not just tidiness: without it these tests
// read the developer's own ~/.config/cloudlab/secrets.yaml, and a
// YubiKey-backed identity would make `go test ./cmd/` block on a
// physical touch. Every test below that clears DIGITALOCEAN_TOKEN must
// pin it, whether or not it wants a fixture.
func writeTokenSecretsFixture(t *testing.T, token string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	recipient := generateTestAgeRecipient(t)

	plainPath := filepath.Join(t.TempDir(), "plain.yaml")
	if err := os.WriteFile(plainPath, []byte("digitalocean_token: "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := exec.Command("sops", "--age", recipient, "-e", plainPath).CombinedOutput()
	if err != nil {
		t.Fatalf("sops -e: %v\n%s", err, enc)
	}

	path, err := secrets.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The environment is the override an unattended agent sets, so it wins
// even when the secrets file has a value -- that is the whole reason
// this precedence runs this way round rather than the other.
func TestResolveToken_PrefersEnvVarOverSecretsFile(t *testing.T) {
	writeTokenSecretsFixture(t, "token-from-secrets-file")
	t.Setenv("DIGITALOCEAN_TOKEN", "token-from-env")

	got, err := resolveToken(context.Background())
	if err != nil {
		t.Fatalf("resolveToken error = %v", err)
	}
	if got != "token-from-env" {
		t.Errorf("token = %q, want the environment's value", got)
	}
}

// Exact equality, not Contains: sops emits the value with a trailing
// newline, and a token handed to the API with one appended fails
// authentication in a way that reads like a bad token.
func TestResolveToken_FallsBackToSecretsFile(t *testing.T) {
	writeTokenSecretsFixture(t, "token-from-secrets-file")
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	got, err := resolveToken(context.Background())
	if err != nil {
		t.Fatalf("resolveToken error = %v", err)
	}
	if got != "token-from-secrets-file" {
		t.Errorf("token = %q, want the secrets file's value", got)
	}
}

// The error is where a reader learns the precedence, so it has to name
// both sources rather than only the one that happened to be checked last.
func TestResolveToken_ErrorNamesBothSources(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	_, err := resolveToken(context.Background())
	if err == nil {
		t.Fatal("expected an error when neither source has a token, got nil")
	}
	for _, want := range []string{"DIGITALOCEAN_TOKEN", "digitalocean_token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %s", err.Error(), want)
		}
	}
}

// status's only use of the API is one field; everything else it prints
// comes from local state. lifecycle.Status already treats a failed live
// check as a fact to report rather than a failure -- an absent token
// belongs in that same category, so status stays usable with no key
// present at all.
func TestStatusCommand_ReportsLocalStateWithoutAToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	sessionTestStore(t, state.Record{
		Name: "myinstance",
		IP:   "203.0.113.7",
		User: "devuser",
		VMID: "12345",
	})

	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	c.SetContext(context.Background())

	if err := runStatus(c, "myinstance", nil); err != nil {
		t.Fatalf("runStatus error = %v", err)
	}
	if !strings.Contains(out.String(), "203.0.113.7") {
		t.Errorf("output = %q, want the IP local state already knows", out.String())
	}
	if !strings.Contains(out.String(), "unknown") {
		t.Errorf("output = %q, want the live status reported as unknown", out.String())
	}
}
