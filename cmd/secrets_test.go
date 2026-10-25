package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/secrets"
	"github.com/jskswamy/bivouac/internal/testenv"
	"github.com/spf13/cobra"
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

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("secrets file not created: %v", err)
	}
}

func TestSecretsInit_FailsWithoutAgeFlag(t *testing.T) {
	testenv.Isolate(t)

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

func TestSecretsSet_PositionalValue(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "test_key", "my_secret_val"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if !strings.Contains(out.String(), `Set secret "test_key" in `+path) {
		t.Errorf("output = %q, want confirmation with key and path", out.String())
	}

	got, err := secrets.Decrypt(context.Background(), path, "test_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "my_secret_val" {
		t.Errorf("Decrypt() = %q, want %q", string(got), "my_secret_val")
	}
}

func TestSecretsSet_FileFlag(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	fileContent := `{"token": "xyz-file-token"}`
	tmpFile := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(tmpFile, []byte(fileContent), 0o600); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "file_key", "--file", tmpFile})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	got, err := secrets.Decrypt(context.Background(), path, "file_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != fileContent {
		t.Errorf("Decrypt() = %q, want %q", string(got), fileContent)
	}
}

func TestSecretsSet_MissingFileFlag_FailsBeforeAutoInit(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	nonExistentFile := filepath.Join(t.TempDir(), "missing-secret.txt")

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "my_key", "--file", nonExistentFile, "--age", recipient})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when --file points to a non-existent file")
	}

	// Verify secrets file was NOT created
	if _, statErr := os.Stat(path); statErr == nil || !os.IsNotExist(statErr) {
		t.Fatalf("expected secrets file %s not to be created, but it was: %v", path, statErr)
	}
}

func TestSecretsSet_FileFlagDirectory_Fails(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	dirPath := t.TempDir()
	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "my_key", "--file", dirPath, "--age", recipient})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when --file points to a directory")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("error = %q, want 'is a directory'", err.Error())
	}
}

func TestSecretsSet_EmptyPositionalValue_FailsBeforeAutoInit(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "empty_key", "", "--age", recipient})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when positional value is empty")
	}
	if !strings.Contains(err.Error(), "secret value cannot be empty") {
		t.Errorf("error = %q, want 'secret value cannot be empty'", err.Error())
	}

	// Verify secrets file was NOT created
	if _, statErr := os.Stat(path); statErr == nil || !os.IsNotExist(statErr) {
		t.Fatalf("expected secrets file %s not to be created, but it was: %v", path, statErr)
	}
}

func TestSecretsSet_StdinPipe(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	// 1. Without positional value, reading stdin
	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "stdin_pipe_key"})
	root.SetIn(strings.NewReader("piped_secret_data"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	got, err := secrets.Decrypt(context.Background(), path, "stdin_pipe_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "piped_secret_data" {
		t.Errorf("Decrypt() = %q, want %q", string(got), "piped_secret_data")
	}

	// 2. With positional "-"
	rootDash := newRootCmd()
	rootDash.SetArgs([]string{"secrets", "set", "stdin_dash_key", "-"})
	rootDash.SetIn(strings.NewReader("dash_secret_data"))
	var outDash bytes.Buffer
	rootDash.SetOut(&outDash)
	rootDash.SetErr(&outDash)

	if err := rootDash.Execute(); err != nil {
		t.Fatalf("secrets set - error = %v: %s", err, outDash.String())
	}

	gotDash, err := secrets.Decrypt(context.Background(), path, "stdin_dash_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(gotDash) != "dash_secret_data" {
		t.Errorf("Decrypt() = %q, want %q", string(gotDash), "dash_secret_data")
	}

	// 3. With --file -
	rootFileDash := newRootCmd()
	rootFileDash.SetArgs([]string{"secrets", "set", "stdin_file_dash_key", "--file", "-"})
	rootFileDash.SetIn(strings.NewReader("file_dash_secret_data"))
	var outFileDash bytes.Buffer
	rootFileDash.SetOut(&outFileDash)
	rootFileDash.SetErr(&outFileDash)

	if err := rootFileDash.Execute(); err != nil {
		t.Fatalf("secrets set --file - error = %v: %s", err, outFileDash.String())
	}

	gotFileDash, err := secrets.Decrypt(context.Background(), path, "stdin_file_dash_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(gotFileDash) != "file_dash_secret_data" {
		t.Errorf("Decrypt() = %q, want %q", string(gotFileDash), "file_dash_secret_data")
	}
}

func TestSecretsSet_OverwriteRefusedWithoutForce(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if err := secrets.Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := secrets.Set(context.Background(), path, "my_key", []byte("orig_val")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// In non-interactive test, overwrite without --force should fail
	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "my_key", "new_val"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("secrets set expected error when overwriting without --force")
	}
	if !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to explain key already exists and suggest --force", err.Error())
	}

	// Verify original value remains
	got, err := secrets.Decrypt(context.Background(), path, "my_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "orig_val" {
		t.Errorf("Decrypt() = %q, want unchanged %q", string(got), "orig_val")
	}
}

func TestSecretsSet_OverwriteAllowedWithForce(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if err := secrets.Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := secrets.Set(context.Background(), path, "my_key", []byte("orig_val")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "my_key", "new_val", "--force"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set with --force error = %v: %s", err, out.String())
	}

	got, err := secrets.Decrypt(context.Background(), path, "my_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "new_val" {
		t.Errorf("Decrypt() = %q, want updated %q", string(got), "new_val")
	}
}

func TestSecretsSet_AutoInitWithAgeFlag(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	// Secrets file does NOT exist
	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected secrets file to not exist yet: %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "auto_init_key", "initial_val", "--age", recipient})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set with --age error = %v: %s", err, out.String())
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected secrets file to be created: %v", err)
	}

	got, err := secrets.Decrypt(context.Background(), path, "auto_init_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "initial_val" {
		t.Errorf("Decrypt() = %q, want %q", string(got), "initial_val")
	}
}

func TestSecretsSet_RejectsInvalidKey(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if err := secrets.Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	invalidKeys := []string{"foo-bar", "foo.bar", "sops", "foo bar", "foo:bar", "foo/bar"}
	for _, k := range invalidKeys {
		root := newRootCmd()
		root.SetArgs([]string{"secrets", "set", k, "val"})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)

		err := root.Execute()
		if err == nil {
			t.Errorf("secrets set key=%q error = nil, want invalid key error", k)
			continue
		}
		wantErr := `keys must match ^[A-Za-z0-9_]+$ and cannot be "sops"`
		if !strings.Contains(err.Error(), wantErr) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), wantErr)
		}
	}
}

func TestSecretsSet_RejectsEmptyValue(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if err := secrets.Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "empty_key", ""})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("secrets set with empty value error = nil, want error")
	}
	if !strings.Contains(err.Error(), "secret value cannot be empty") {
		t.Errorf("error = %q, want 'secret value cannot be empty'", err.Error())
	}
}

func TestSecretsSet_AutoInitWithoutAgeNonInteractive_Fails(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "any_key", "any_val"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when secrets file doesn't exist and no --age flag")
	}
	if !strings.Contains(err.Error(), "doesn't exist yet") || !strings.Contains(err.Error(), "bivouac secrets init --age") {
		t.Errorf("error = %q, want actionable message directing to secrets init --age", err.Error())
	}
}

func TestSecretsSet_EditorFlag(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	origEditor := openInEditor
	t.Cleanup(func() { openInEditor = origEditor })
	openInEditor = func(_ context.Context, _ *cobra.Command) ([]byte, error) {
		return []byte("editor_secret_content"), nil
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "editor_key", "--editor"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set --editor error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	got, err := secrets.Decrypt(context.Background(), path, "editor_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "editor_secret_content" {
		t.Errorf("Decrypt() = %q, want %q", string(got), "editor_secret_content")
	}
}

func TestSecretsSet_InteractivePrompt_Paste(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	origTerm := secretsStdinIsTerminal
	t.Cleanup(func() { secretsStdinIsTerminal = origTerm })
	secretsStdinIsTerminal = func() bool { return true }

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "pasted_key"})
	root.SetIn(strings.NewReader("pasted_token_line1\npasted_token_line2\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set interactive paste error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	got, err := secrets.Decrypt(context.Background(), path, "pasted_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	want := "pasted_token_line1\npasted_token_line2"
	if string(got) != want {
		t.Errorf("Decrypt() = %q, want %q", string(got), want)
	}
}

func TestSecretsSet_InteractivePrompt_EnterLaunchesEditor(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	initRoot := newRootCmd()
	initRoot.SetArgs([]string{"secrets", "init", "--age", recipient})
	if err := initRoot.Execute(); err != nil {
		t.Fatalf("secrets init error = %v", err)
	}

	origTerm := secretsStdinIsTerminal
	origEditor := openInEditor
	t.Cleanup(func() {
		secretsStdinIsTerminal = origTerm
		openInEditor = origEditor
	})
	secretsStdinIsTerminal = func() bool { return true }
	openInEditor = func(_ context.Context, _ *cobra.Command) ([]byte, error) {
		return []byte("from_interactive_editor"), nil
	}

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "enter_editor_key"})
	root.SetIn(strings.NewReader("\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set error = %v: %s", err, out.String())
	}

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	got, err := secrets.Decrypt(context.Background(), path, "enter_editor_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "from_interactive_editor" {
		t.Errorf("Decrypt() = %q, want %q", string(got), "from_interactive_editor")
	}
}

func TestSecretsSet_InteractiveOverwrite_ConfirmAndAbort(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")
	if err := secrets.Init(context.Background(), path, []string{recipient}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := secrets.Set(context.Background(), path, "my_key", []byte("orig_val")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	origTerm := secretsStdinIsTerminal
	t.Cleanup(func() { secretsStdinIsTerminal = origTerm })
	secretsStdinIsTerminal = func() bool { return true }

	// Test Abort (answer 'n')
	rootAbort := newRootCmd()
	rootAbort.SetArgs([]string{"secrets", "set", "my_key", "aborted_val"})
	rootAbort.SetIn(strings.NewReader("n\n"))
	var outAbort bytes.Buffer
	rootAbort.SetOut(&outAbort)
	rootAbort.SetErr(&outAbort)

	if err := rootAbort.Execute(); err != nil {
		t.Fatalf("expected nil error on abort, got %v", err)
	}
	if !strings.Contains(outAbort.String(), "Aborted.") {
		t.Errorf("output = %q, want it to contain 'Aborted.'", outAbort.String())
	}

	got, err := secrets.Decrypt(context.Background(), path, "my_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "orig_val" {
		t.Errorf("Decrypt() = %q, want unchanged 'orig_val'", string(got))
	}

	// Test Confirm (answer 'y')
	rootConfirm := newRootCmd()
	rootConfirm.SetArgs([]string{"secrets", "set", "my_key", "confirmed_val"})
	rootConfirm.SetIn(strings.NewReader("y\n"))
	var outConfirm bytes.Buffer
	rootConfirm.SetOut(&outConfirm)
	rootConfirm.SetErr(&outConfirm)

	if err := rootConfirm.Execute(); err != nil {
		t.Fatalf("expected success on confirm, got %v: %s", err, outConfirm.String())
	}

	gotConfirmed, err := secrets.Decrypt(context.Background(), path, "my_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(gotConfirmed) != "confirmed_val" {
		t.Errorf("Decrypt() = %q, want 'confirmed_val'", string(gotConfirmed))
	}
}

func TestSecretsSet_InteractiveAutoInit_Prompt(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	recipient := generateTestAgeRecipient(t)

	origTerm := secretsStdinIsTerminal
	t.Cleanup(func() { secretsStdinIsTerminal = origTerm })
	secretsStdinIsTerminal = func() bool { return true }

	path := filepath.Join(configDir, "bivouac", "secrets.yaml")

	root := newRootCmd()
	root.SetArgs([]string{"secrets", "set", "interactive_init_key", "val_123"})
	root.SetIn(strings.NewReader(recipient + "\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("secrets set error = %v: %s", err, out.String())
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected secrets file created: %v", err)
	}

	got, err := secrets.Decrypt(context.Background(), path, "interactive_init_key")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(got) != "val_123" {
		t.Errorf("Decrypt() = %q, want 'val_123'", string(got))
	}
}
