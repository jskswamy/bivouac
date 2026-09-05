# Tailscale Auto-Join and Moshi Pairing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `cloudlab tailscale`, `cloudlab pair`, and `cloudlab secrets` commands, plus an optional Tailscale auto-join step in `up`, so instances can join a private tailnet and pair with the getmoshi.app mobile app without any manual SSH step.

**Architecture:** A new `internal/secrets` package handles a personal, sops-encrypted YAML file (age/YubiKey recipients) with just-in-time decrypt-and-zero semantics. `internal/lifecycle` gains `JoinTailscale` and `Pair`, following the exact thin-wrapper shape `Herdr`/`Tmux`/`SSH` already establish. `cloudlab.pkl` gains a `tailscale` boolean that gates an optional step in `Up`; `state.Record` gains `TailscaleJoined` so `down` knows whether to deregister, independent of the current config value.

**Tech Stack:** Go, `sops`/`age`/`age-plugin-yubikey` (shelled out to, not linked), `golang.org/x/crypto/ssh` (via the existing `internal/reconcile.Client`), Pkl/`pkl-go` codegen, Cobra.

## Global Constraints

- Format is YAML, not TOML — `sops` has no TOML support (`sops --help`: "currently json, yaml, dotenv, ini and binary"); confirmed empirically that a `.toml` file falls back to `binary` mode and breaks per-key `--extract`.
- The Tailscale auth key must never appear as a literal CLI argument (locally or on the remote instance) and never touch disk in plaintext. It moves only over SSH stdin into a tmpfs path, and is captured as `[]byte` (never `string`) so it can be explicitly zeroed after use.
- `down`'s Tailscale-logout decision reads `state.Record.TailscaleJoined`, never a freshly-resolved `cloudlab.pkl` — `down.go`'s `Down()` never receives a `config.Config`, and the config value could have changed or diverged from what actually happened.
- Every new lifecycle function follows the existing `Herdr`/`Tmux`/`SSH` shape: a pure, unit-tested argv/script builder plus a thin `exec.Command`/`session.Run` wrapper with stdio passthrough for interactive commands — no new abstractions.
- External tools (`sops`, `tailscale`, `ssh`, `age-plugin-yubikey`) are always invoked as subprocesses, never linked as Go libraries — matches this codebase's existing convention for `mutagen`/`nix`/`tailscale` already.

---

### Task 1: Add `tailscale` config field to `cloudlab.pkl`

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify: `internal/config/Config.pkl.go` (regenerated, not hand-edited)
- Modify: `internal/config/config.go:mergeConfig`
- Modify: `internal/config/config_test.go`
- Modify: `docs/config.md`

**Interfaces:**
- Produces: `config.Config.Tailscale bool` (`pkl:"tailscale"`), consumed by Task 6 (`lifecycle.Up`).

- [ ] **Step 1: Add the field to the Pkl schema**

In `internal/config/Config.pkl`, add `tailscale` alongside `arch`/`image` (same "has a default, project's value always wins" category — not a pointer, not additive):

```pkl
arch: "x86_64"|"arm64" = "x86_64"
image: String = "ubuntu-24-04-x64"
tailscale: Boolean = false
sshKeys: Listing<String>?
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `make generate`

This runs `nix develop --command go generate ./...`, which re-invokes `pkl run ... gen.pkl` against `Config.pkl`. Verify `internal/config/Config.pkl.go` now contains:

```go
Tailscale bool `pkl:"tailscale"`
```

as a new field in the `Config` struct (placed by the generator wherever it lands relative to `Arch`/`Image`/`SshKeys` — don't hand-reorder it).

- [ ] **Step 3: Wire it into the merge**

In `internal/config/config.go`, `mergeConfig` currently has:

```go
		Arch:     project.Arch,
		Image:    project.Image,
```

Add, in the same style (project's value always wins, base never consulted):

```go
		Arch:      project.Arch,
		Image:     project.Image,
		Tailscale: project.Tailscale,
```

- [ ] **Step 4: Extend the existing merge test**

In `internal/config/config_test.go`, `TestLoad_MergesWithBase_ScalarsOverrideListsAdditive`'s project fixture already sets `arch = "arm64"` to prove project-wins passthrough. Add `tailscale = true` right next to it:

```go
	project := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, project, strings.Join([]string{
		`basePath = "./base.pkl"`,
		`size = "s-2vcpu-4gb"`, // overrides base
		`template = "python"`,
		`arch = "arm64"`,
		`tailscale = true`,
		`sshKeys { "project-key" }`,
		`packages { "ripgrep" }`,
	}, "\n")+"\n")
```

And add the assertion right after the existing `Arch` check:

```go
	if cfg.Arch != "arm64" {
		t.Errorf("Arch = %q, want %q (project overrides base)", cfg.Arch, "arm64")
	}
	if !cfg.Tailscale {
		t.Error("Tailscale = false, want true (project overrides base)")
	}
```

- [ ] **Step 5: Run the config package tests**

Run: `nix develop --command go test ./internal/config/...`
Expected: PASS (requires the real `pkl` CLI on PATH, same as every other test in this package).

- [ ] **Step 6: Document the field**

In `docs/config.md`'s field table, add a row after `image`:

```markdown
| `tailscale` | `Boolean` | No | `false` | Auto-join the instance to your personal Tailscale network during `up` (see `cloudlab tailscale`). Requires `tailscale_authkey` in your personal secrets file — see `cloudlab secrets init`. |
```

- [ ] **Step 7: Commit**

```bash
git add internal/config/Config.pkl internal/config/Config.pkl.go internal/config/config.go internal/config/config_test.go docs/config.md
git commit -m "Add tailscale config field, off by default"
```

---

### Task 2: `internal/secrets` — Path, Decrypt, Zero

**Files:**
- Create: `internal/secrets/secrets.go`
- Create: `internal/secrets/secrets_test.go`

**Interfaces:**
- Produces: `secrets.Path() (string, error)`, `secrets.Decrypt(ctx context.Context, path, key string) ([]byte, error)`, `secrets.Zero(b []byte)`. Consumed by Task 5 (`lifecycle.JoinTailscale`) and Task 3 (this package's own `Keys`/`Init`).

- [ ] **Step 1: Write the failing tests**

Create `internal/secrets/secrets_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/secrets/... -run 'TestDecrypt|TestZero|TestPath' -v`
Expected: FAIL (package doesn't exist yet / functions undefined).

- [ ] **Step 3: Write the implementation**

Create `internal/secrets/secrets.go`:

```go
// Package secrets manages cloudlab's personal, sops-encrypted secrets
// file (Tailscale auth keys today, whatever else needs one later):
// decrypting individual values just-in-time, listing key names, and
// creating the file for a fresh setup. Every operation shells out to
// sops -- it is never linked as a Go library, matching this
// codebase's existing convention for tailscale/mutagen/nix.
package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Path returns the location of cloudlab's personal secrets file:
// $XDG_CONFIG_HOME/cloudlab/secrets.yaml if set, else
// ~/.config/cloudlab/secrets.yaml -- same XDG convention as
// internal/config's resolveBasePath.
func Path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cloudlab", "secrets.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cloudlab", "secrets.yaml"), nil
}

// Decrypt returns the decrypted value of key in the sops-encrypted
// YAML file at path, as a byte slice the caller must Zero immediately
// after its one use. Fails with a specific error if path doesn't
// exist yet (checked before ever invoking sops); a missing key
// surfaces sops's own "component [...] not found" message.
func Decrypt(ctx context.Context, path, key string) ([]byte, error) {
	if _, err := exec.LookPath("sops"); err != nil {
		return nil, fmt.Errorf("sops not found on PATH: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("secrets file %s doesn't exist yet (run `cloudlab secrets init`)", path)
		}
		return nil, err
	}
	// #nosec G204 -- argv-array exec.Command, no shell; path is
	// cloudlab's own fixed secrets path and key is a fixed constant
	// supplied by callers in this codebase, never external input.
	cmd := exec.CommandContext(ctx, "sops", "-d", "--extract", fmt.Sprintf(`["%s"]`, key), path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops -d --extract %q %s: %w\n%s", key, path, err, stderr.String())
	}
	return out, nil
}

// Zero overwrites b's contents with zero bytes in place, so a
// decrypted secret doesn't linger in memory past its one use.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/secrets/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/secrets/secrets.go internal/secrets/secrets_test.go
git commit -m "Add internal/secrets: Path, Decrypt, Zero"
```

---

### Task 3: `internal/secrets` — Keys, Init, Edit

**Files:**
- Modify: `internal/secrets/secrets.go`
- Modify: `internal/secrets/secrets_test.go`

**Interfaces:**
- Consumes: nothing new from other tasks.
- Produces: `secrets.Keys(path string) ([]string, error)`, `secrets.Init(ctx context.Context, path string, recipients []string) error`, `secrets.Edit(ctx context.Context, path string) error`. Consumed by Task 10 (`cloudlab secrets` command).

- [ ] **Step 1: Write the failing tests**

Add to `internal/secrets/secrets_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/secrets/... -run 'TestKeys|TestInit' -v`
Expected: FAIL (`Keys`/`Init` undefined).

- [ ] **Step 3: Write the implementation**

Add to `internal/secrets/secrets.go` (extend the existing `import` block with `bufio`, `os` is already there, `regexp`, `sort`, `strings`):

```go
import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)
```

```go
// topLevelKeyPattern matches a top-level "key:" line in a
// sops-encrypted YAML file. sops only encrypts values, not keys, so
// Keys reads the file directly with no decryption at all. Deliberately
// a simple line scan, not a YAML parser: this file is meant to stay a
// flat list of secrets, never nested mappings, so pulling in a YAML
// dependency for one command isn't worth it.
var topLevelKeyPattern = regexp.MustCompile(`^([A-Za-z0-9_]+):`)

// Keys returns the top-level key names in the secrets file at path,
// excluding sops's own "sops" metadata key, sorted alphabetically.
func Keys(path string) ([]string, error) {
	// #nosec G304 -- path is always secrets.Path()'s fixed location
	// (or a caller-trusted equivalent in tests), never external input.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("secrets file %s doesn't exist yet (run `cloudlab secrets init`)", path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var keys []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := topLevelKeyPattern.FindStringSubmatch(scanner.Text())
		if m == nil || m[1] == "sops" {
			continue
		}
		keys = append(keys, m[1])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// Init creates path as a fresh sops-encrypted secrets file with no
// keys yet, encrypted for recipients (age public keys or
// age-plugin-yubikey identities, comma-joined into sops's own --age
// flag). Fails if path already exists -- use Edit to change one.
func Init(ctx context.Context, path string, recipients []string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("at least one --age recipient is required")
	}
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops not found on PATH: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists (use `cloudlab secrets edit` to change it)", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// #nosec G204 -- argv-array exec.Command, no shell; recipients are
	// age public keys supplied via cloudlab's own --age flag, passed
	// through to sops's identical flag, never shell-interpreted.
	cmd := exec.CommandContext(ctx, "sops",
		"--age", strings.Join(recipients, ","),
		"--input-type", "yaml", "--output-type", "yaml",
		"-e", "/dev/stdin")
	cmd.Stdin = strings.NewReader("{}\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sops -e: %w\n%s", err, stderr.String())
	}
	return os.WriteFile(path, out, 0o600)
}

// Edit opens path in $EDITOR via sops's own edit flow (decrypt,
// $EDITOR, re-encrypt), execing the real sops binary with stdio
// passed straight through -- same thin-client shape as
// lifecycle.SSH/Herdr/Tmux, just not instance-scoped.
func Edit(ctx context.Context, path string) error {
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops not found on PATH: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("secrets file %s doesn't exist yet (run `cloudlab secrets init`)", path)
		}
		return err
	}
	// #nosec G204 -- argv-array exec.Command, no shell; path is
	// cloudlab's own fixed secrets path, never external input.
	cmd := exec.CommandContext(ctx, "sops", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/secrets/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/secrets/secrets.go internal/secrets/secrets_test.go
git commit -m "Add internal/secrets: Keys, Init, Edit"
```

---

### Task 4: `reconcile.Client.WriteSecretFile`

**Files:**
- Modify: `internal/reconcile/ssh.go`
- Modify: `internal/reconcile/ssh_test.go`

**Interfaces:**
- Produces: `(*Client).WriteSecretFile(remotePath string, content []byte) error`. Consumed by Task 5 (`lifecycle.JoinTailscale`).

- [ ] **Step 1: Write the failing test**

Add to `internal/reconcile/ssh_test.go`:

```go
func TestClient_WriteSecretFile_SendsContentViaStdinWithRestrictedMode(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got sessionResult
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		got = sessionResult{Command: cmd, Stdin: stdin}
		return "", 0
	})

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	secret := []byte("tskey-abc123-example")
	if err := client.WriteSecretFile("/run/user/1000/cloudlab-ts-authkey", secret); err != nil {
		t.Fatalf("WriteSecretFile() error = %v", err)
	}

	if !strings.Contains(got.Command, "install -m 600") {
		t.Errorf("command = %q, want it to use install -m 600", got.Command)
	}
	if !strings.Contains(got.Command, "/run/user/1000/cloudlab-ts-authkey") {
		t.Errorf("command = %q, want it to reference the target path", got.Command)
	}
	if string(got.Stdin) != "tskey-abc123-example" {
		t.Errorf("stdin sent = %q, want %q (the secret content)", got.Stdin, "tskey-abc123-example")
	}
	if strings.Contains(got.Command, "tskey-abc123-example") {
		t.Error("command string contains the secret literal -- it must only travel via stdin")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/reconcile/... -run TestClient_WriteSecretFile -v`
Expected: FAIL (`WriteSecretFile` undefined).

- [ ] **Step 3: Write the implementation**

Add to `internal/reconcile/ssh.go`, right after the existing `WriteFile` method:

```go
// WriteSecretFile writes content to remotePath on the instance with
// permissions restricted to the owning user from the moment the file
// exists (mode 600 via install, rather than cat followed by a
// separate chmod, which would briefly leave default-umask
// permissions). content is streamed over the SSH session's stdin --
// unlike WriteFile's content-as-string approach (fine for a rendered
// flake.nix, wrong for anything sensitive), it never appears as a
// literal command argument.
func (c *Client) WriteSecretFile(remotePath string, content []byte) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	defer func() { _ = session.Close() }()

	session.Stdin = bytes.NewReader(content)
	cmd := fmt.Sprintf("install -m 600 /dev/stdin %s", ShellQuote(remotePath))
	if out, err := session.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("writing %s: %w\n%s", remotePath, err, out)
	}
	return nil
}
```

(`bytes` and `fmt` are already imported in this file.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/reconcile/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/reconcile/ssh.go internal/reconcile/ssh_test.go
git commit -m "Add Client.WriteSecretFile for stdin-only secret writes"
```

---

### Task 5: `state.Record.TailscaleJoined` + `lifecycle.JoinTailscale`

**Files:**
- Modify: `internal/state/state.go`
- Create: `internal/lifecycle/tailscale.go`
- Create: `internal/lifecycle/tailscale_test.go`

**Interfaces:**
- Consumes: `secrets.Path`/`Decrypt`/`Zero` (Task 2), `reconcile.Connect`/`Client.Run`/`Client.WriteSecretFile` (Task 4).
- Produces: `state.Record.TailscaleJoined bool`, `lifecycle.JoinTailscale(ctx context.Context, ip, user string) error`. Consumed by Task 6 (`Up`), Task 7 (`cloudlab tailscale`), Task 8 (`down`).

- [ ] **Step 1: Add the state field**

In `internal/state/state.go`, add to `Record`:

```go
type Record struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	VMID            string `json:"vm_id"`
	IP              string `json:"ip"`
	Region          string `json:"region"`
	Size            string `json:"size"`
	Template        string `json:"template"`
	User            string `json:"user"`
	RepoPath        string `json:"repo_path"`
	WatchPID        int    `json:"watch_pid"`
	TunnelPID       int    `json:"tunnel_pid"`
	TailscaleJoined bool   `json:"tailscale_joined"`
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/lifecycle/tailscale_test.go`:

```go
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
		if strings.HasPrefix(cmd, "printf") {
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
	if !strings.Contains(commands[1], "install -m 600") || !strings.Contains(commands[1], "/run/user/1000/cloudlab-ts-authkey") {
		t.Errorf("commands[1] = %q, want an install -m 600 into the resolved runtime dir", commands[1])
	}
	if string(stdins[1]) != "tskey-abc123-example" {
		t.Errorf("stdin to the write step = %q, want the decrypted auth key", stdins[1])
	}
	if !strings.Contains(commands[2], "tailscale up --auth-key=file:") || !strings.Contains(commands[2], "/run/user/1000/cloudlab-ts-authkey") {
		t.Errorf("commands[2] = %q, want tailscale up --auth-key=file:<path>", commands[2])
	}
	if !strings.Contains(commands[2], "trap") {
		t.Errorf("commands[2] = %q, want a trap to clean up the tmpfs file", commands[2])
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
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/lifecycle/... -run TestJoinTailscale -v`
Expected: FAIL (`JoinTailscale` undefined).

- [ ] **Step 4: Write the implementation**

Create `internal/lifecycle/tailscale.go`:

```go
package lifecycle

import (
	"context"
	"fmt"

	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/secrets"
)

// JoinTailscale joins the instance at ip (connecting as user) to the
// tailnet: decrypts the auth key from the local secrets file just in
// time, resolves the instance's own $XDG_RUNTIME_DIR (a tmpfs path,
// confirmed present in a non-interactive SSH session via pam_systemd
// -- the same directory moshi-hook's own socket already lives in),
// streams the key there over SSH stdin (never a literal argument
// anywhere, never touching disk in plaintext on either end), runs
// tailscale up against it, and zeroes the local copy immediately
// after the remote write completes -- regardless of whether the
// subsequent tailscale up call succeeds.
//
// The runtime dir is resolved via a real remote round-trip rather
// than assumed or interpolated unresolved into a later command: the
// resulting path is a concrete literal, so it can be safely
// shell-quoted like any other WriteSecretFile/Run argument instead of
// needing an unquoted special case for shell-expansion syntax.
func JoinTailscale(ctx context.Context, ip, user string) error {
	path, err := secrets.Path()
	if err != nil {
		return err
	}
	key, err := secrets.Decrypt(ctx, path, "tailscale_authkey")
	if err != nil {
		return fmt.Errorf("decrypting tailscale_authkey: %w", err)
	}
	defer secrets.Zero(key)

	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	runtimeDir, err := client.Run(`printf '%s' "$XDG_RUNTIME_DIR"`)
	if err != nil {
		return fmt.Errorf("resolving instance's runtime directory: %w", err)
	}
	if runtimeDir == "" {
		return fmt.Errorf("instance has no $XDG_RUNTIME_DIR (no active login session for %s?)", user)
	}
	authKeyPath := runtimeDir + "/cloudlab-ts-authkey"

	writeErr := client.WriteSecretFile(authKeyPath, key)
	secrets.Zero(key)
	if writeErr != nil {
		return fmt.Errorf("writing auth key to instance: %w", writeErr)
	}

	script := fmt.Sprintf(`trap 'rm -f %s' EXIT; tailscale up --auth-key=file:%s`,
		reconcile.ShellQuote(authKeyPath), reconcile.ShellQuote(authKeyPath))
	if out, err := client.Run(script); err != nil {
		return fmt.Errorf("tailscale up failed: %w\n%s", err, out)
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/lifecycle/... -run TestJoinTailscale -v`
Expected: PASS

- [ ] **Step 6: Run the full lifecycle and state suites**

Run: `go test ./internal/lifecycle/... ./internal/state/... -v`
Expected: PASS (confirms the new `Record` field doesn't break existing JSON round-trips).

- [ ] **Step 7: Commit**

```bash
git add internal/state/state.go internal/lifecycle/tailscale.go internal/lifecycle/tailscale_test.go
git commit -m "Add lifecycle.JoinTailscale and state.Record.TailscaleJoined"
```

---

### Task 6: Wire `JoinTailscale` into `Up`

**Files:**
- Modify: `internal/lifecycle/lifecycle.go`
- Modify: `internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `lifecycle.JoinTailscale` (Task 5), `config.Config.Tailscale` (Task 1).
- Produces: `Steps.JoinTailscale func(ctx context.Context, ip, user string) error` field.

- [ ] **Step 1: Write the failing tests**

Add to `internal/lifecycle/lifecycle_test.go`:

```go
func TestUp_JoinsTailscaleWhenConfigEnablesIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := filepath.Join(repoRoot, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`tailscale = true`,
	}, "\n")+"\n")

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	var gotIP, gotUser string
	steps := Steps{
		WaitReady: func(ctx context.Context, ip string, timeout time.Duration) error { return nil },
		Reconcile: func(ctx context.Context, name, cloudlabPath string) error { return nil },
		JoinTailscale: func(ctx context.Context, ip, user string) error {
			gotIP, gotUser = ip, user
			return nil
		},
		Rsync:      func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error { return nil },
		StartWatch: func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error { return nil },
	}

	if err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if gotIP != "192.0.2.1" || gotUser == "" {
		t.Errorf("JoinTailscale called with (%q, %q)", gotIP, gotUser)
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("myinstance")
	if err != nil || !ok {
		t.Fatalf("state.Get(myinstance) ok=%v err=%v", ok, err)
	}
	if !record.TailscaleJoined {
		t.Error("record.TailscaleJoined = false, want true after a successful join")
	}
}

func TestUp_SkipsTailscaleWhenConfigDisablesIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	repoRoot := t.TempDir()
	cloudlabPath := minimalCloudlabPkl(t, repoRoot)

	p := &fakeProvider{vm: provider.VM{ID: "vm-1", IP: "192.0.2.1", Region: "nyc3", Size: "s-1vcpu-1gb"}}

	called := false
	steps := Steps{
		WaitReady:     func(ctx context.Context, ip string, timeout time.Duration) error { return nil },
		Reconcile:     func(ctx context.Context, name, cloudlabPath string) error { return nil },
		JoinTailscale: func(ctx context.Context, ip, user string) error { called = true; return nil },
		Rsync:         func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error { return nil },
		StartWatch:    func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error { return nil },
	}

	if err := Up(context.Background(), p, steps, "myinstance", cloudlabPath, repoRoot); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if called {
		t.Error("JoinTailscale was called despite tailscale defaulting to false")
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Get("myinstance")
	if err != nil || !ok {
		t.Fatalf("state.Get(myinstance) ok=%v err=%v", ok, err)
	}
	if record.TailscaleJoined {
		t.Error("record.TailscaleJoined = true, want false when tailscale was never enabled")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lifecycle/... -run TestUp_.*Tailscale -v`
Expected: FAIL (`Steps.JoinTailscale` field doesn't exist).

- [ ] **Step 3: Write the implementation**

In `internal/lifecycle/lifecycle.go`, add `JoinTailscale` to the `Steps` struct:

```go
type Steps struct {
	WaitReady     func(ctx context.Context, ip string, timeout time.Duration) error
	Reconcile     func(ctx context.Context, name, cloudlabPath string) error
	JoinTailscale func(ctx context.Context, ip, user string) error
	Rsync         func(ctx context.Context, ip, user, localRepoRoot, remotePath string) error
	StartWatch    func(ctx context.Context, ip, user, name, localRepoRoot, remotePath string) error
}
```

Wire it into `DefaultSteps`:

```go
func DefaultSteps() Steps {
	return Steps{
		WaitReady: WaitReady,
		Reconcile: func(ctx context.Context, name, cloudlabPath string) error {
			return tui.Run(ctx, "Reconciling environment", func(ctx context.Context) error {
				return reconcile.Reconcile(ctx, name, cloudlabPath)
			})
		},
		JoinTailscale: JoinTailscale,
		Rsync:         Rsync,
		StartWatch:    StartWatch,
	}
}
```

And in `Up`, right after the existing `steps.Reconcile(...)` call and before `steps.Rsync(...)`:

```go
	if err := steps.Reconcile(ctx, name, cloudlabPath); err != nil {
		return err
	}

	if cfg.Tailscale {
		if err := steps.JoinTailscale(ctx, vm.IP, remoteUser); err != nil {
			return err
		}
		record.TailscaleJoined = true
		if err := store.Put(record); err != nil {
			return err
		}
	}

	if err := steps.Rsync(ctx, vm.IP, remoteUser, repoRoot, remotePath); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/lifecycle/... -v`
Expected: PASS (including every pre-existing `TestUp_*` test — their `Steps{}` literals simply leave `JoinTailscale` nil, which is never called since their fixtures don't set `tailscale = true`).

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/lifecycle.go internal/lifecycle/lifecycle_test.go
git commit -m "Run JoinTailscale as an optional step in Up"
```

---

### Task 7: `cloudlab tailscale [name]` command

**Files:**
- Modify: `cmd/lookup.go`
- Modify: `cmd/lookup_run.go`
- Modify: `cmd/lookup_test.go`

**Interfaces:**
- Consumes: `lifecycle.JoinTailscale` (Task 5), `state.Record.TailscaleJoined` (Task 5).

- [ ] **Step 1: Write the failing test**

In `cmd/lookup_test.go`, add a row to `TestLookupCommands_NameFlagResolves`'s `cases` table:

```go
		{[]string{"tailscale", "--name", "myrepo"}, "tailscale", `no instance named "myrepo"`},
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/... -run TestLookupCommands_NameFlagResolves -v`
Expected: FAIL (`tailscale` isn't a registered command yet — cobra reports "unknown command").

- [ ] **Step 3: Write the implementation**

In `cmd/lookup.go`, add a new entry to `lookupCommandSpecs` (after `tmux`, before `watch` — keeps the interactive-session commands grouped):

```go
	{
		use:   "tailscale [name]",
		short: "Join the instance to your personal Tailscale network",
		verb:  "tailscale",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runTailscale,
	},
```

In `cmd/lookup_run.go`, add (near `runHerdr`):

```go
func runTailscale(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	if err := lifecycle.JoinTailscale(cmd.Context(), record.IP, record.User); err != nil {
		return err
	}
	record.TailscaleJoined = true
	if err := store.Put(record); err != nil {
		return err
	}
	cmd.Printf("%s joined the tailnet\n", name)
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/... -run TestLookupCommands_NameFlagResolves -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/lookup.go cmd/lookup_run.go cmd/lookup_test.go
git commit -m "Add cloudlab tailscale command"
```

---

### Task 8: Deregister Tailscale on `down`

**Files:**
- Modify: `internal/lifecycle/down.go`
- Modify: `internal/lifecycle/down_test.go`
- Modify: `docs/superpowers/specs/2026-09-04-tailscale-and-moshi-pairing-design.md` is already accurate here — no spec change needed, this task just implements it.

**Interfaces:**
- Consumes: `reconcile.Connect`/`Client.Run` (existing), `state.Record.TailscaleJoined` (Task 5).

- [ ] **Step 1: Write the failing tests**

Add to `internal/lifecycle/down_test.go`:

```go
func TestDown_DeregistersTailscaleWhenJoined(t *testing.T) {
	store := setupDownTest(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var gotCmd string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		gotCmd = cmd
		return "", 0
	})

	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser", TailscaleJoined: true}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if gotCmd != "tailscale logout" {
		t.Errorf("remote command = %q, want %q", gotCmd, "tailscale logout")
	}
}

func TestDown_SkipsTailscaleLogoutWhenNeverJoined(t *testing.T) {
	store := setupDownTest(t)
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	called := false
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		called = true
		return "", 0
	})

	record := state.Record{Name: "myinstance", VMID: "vm-1", IP: addr, User: "devuser"}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if called {
		t.Error("tailscale logout was run despite TailscaleJoined being false")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lifecycle/... -run TestDown_.*Tailscale -v`
Expected: FAIL (`TestDown_DeregistersTailscaleWhenJoined` fails because nothing calls `tailscale logout` yet — `gotCmd` stays empty).

- [ ] **Step 3: Write the implementation**

In `internal/lifecycle/down.go`, add the import:

```go
	"github.com/jskswamy/cloudlab/internal/reconcile"
```

Add, near `terminateWatch`:

```go
// deregisterTailscale best-effort logs the instance out of its
// tailnet before it's destroyed -- once destroyed, nothing can run on
// it anymore, so this must happen first. Errors are swallowed, same
// as terminateWatch: a failed logout must never block VM teardown.
// Skipped entirely if this instance never actually joined, checked
// via record.TailscaleJoined rather than a freshly-resolved
// cloudlab.pkl -- Down never receives a config.Config, and the
// config's current value could differ from what actually happened
// (the toggle could've changed, or the instance could've been joined
// manually via `cloudlab tailscale` with the config still false).
func deregisterTailscale(ctx context.Context, record state.Record) {
	if !record.TailscaleJoined {
		return
	}
	client, err := reconcile.Connect(ctx, record.IP, record.User)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	_, _ = client.Run("tailscale logout")
}
```

Call it in `Down`, right after `terminateWatch`:

```go
func Down(ctx context.Context, p provider.Provider, store *state.Store, record state.Record) error {
	terminateWatch(ctx, record.Name)
	deregisterTailscale(ctx, record)

	if err := p.Destroy(ctx, record.VMID); err != nil && !errors.Is(err, provider.ErrNotFound) {
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/lifecycle/... -v`
Expected: PASS (including the three pre-existing `TestDown_*` tests, whose records leave `TailscaleJoined` at its zero value `false`).

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle/down.go internal/lifecycle/down_test.go
git commit -m "Deregister Tailscale before destroying a joined instance"
```

---

### Task 9: `lifecycle.Pair` + `cloudlab pair [name]` command

**Files:**
- Create: `internal/lifecycle/pair.go`
- Create: `internal/lifecycle/pair_test.go`
- Modify: `cmd/lookup.go`
- Modify: `cmd/lookup_run.go`
- Modify: `cmd/lookup_test.go`

**Interfaces:**
- Produces: `lifecycle.Pair(ctx context.Context, ip, user string) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/lifecycle/pair_test.go`:

```go
package lifecycle

import (
	"testing"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

func TestPairArgs_BuildsExpectedCommand(t *testing.T) {
	got := pairArgs("203.0.113.5", "devuser")
	inner := "moshi-hook host setup --host " + reconcile.ShellQuote("203.0.113.5")
	want := []string{"-t", "devuser@203.0.113.5", "bash -lc " + reconcile.ShellQuote(inner)}
	if len(got) != len(want) {
		t.Fatalf("pairArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pairArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/lifecycle/... -run TestPairArgs -v`
Expected: FAIL (`pairArgs` undefined).

- [ ] **Step 3: Write the implementation**

Create `internal/lifecycle/pair.go`:

```go
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// pairArgs builds the argv Pair passes to the ssh binary: a PTY
// session as user on ip, running moshi-hook's own Easy Pair QR flow
// with --host pinned to ip itself so the QR always advertises the
// instance's real public address, rather than falling into
// moshi-hook's own interactive address-selector prompt (which would
// have no terminal to prompt on anyway, running non-interactively
// like this). Runs inside a login shell for the same PATH reason
// tmuxArgs does -- moshi-hook lives in the instance user's
// home-manager profile, not a system path.
func pairArgs(ip, user string) []string {
	inner := "moshi-hook host setup --host " + reconcile.ShellQuote(ip)
	return []string{"-t", user + "@" + ip, "bash -lc " + reconcile.ShellQuote(inner)}
}

// Pair runs moshi-hook's Easy Pair QR flow on the instance at ip as
// user, execing the real ssh binary with stdio passed straight
// through -- same shape as Tmux/SSH. Blocks in the foreground showing
// the QR until scanned/claimed, or until Ctrl+C.
func Pair(ctx context.Context, ip, user string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found on PATH: %w", err)
	}
	// #nosec G204 -- argv-array exec.Command locally, no local shell;
	// the remote command IS shell-interpreted by sshd's login shell,
	// but ip is shell-quoted via reconcile.ShellQuote before being
	// embedded, and ip/user are never attacker-controlled regardless.
	cmd := exec.CommandContext(ctx, "ssh", pairArgs(ip, user)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/lifecycle/... -run TestPairArgs -v`
Expected: PASS

- [ ] **Step 5: Write the failing cmd-level test**

In `cmd/lookup_test.go`, add a row to `TestLookupCommands_NameFlagResolves`'s `cases` table:

```go
		{[]string{"pair", "--name", "myrepo"}, "pair", `no instance named "myrepo"`},
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./cmd/... -run TestLookupCommands_NameFlagResolves -v`
Expected: FAIL (`pair` isn't a registered command yet).

- [ ] **Step 7: Wire the command**

In `cmd/lookup.go`, add a new entry to `lookupCommandSpecs` (after `tailscale`):

```go
	{
		use:   "pair [name]",
		short: "Pair the getmoshi.app mobile app with the instance via QR code",
		verb:  "pair",
		args:  cobra.MaximumNArgs(1),
		named: true,
		run:   runPair,
	},
```

In `cmd/lookup_run.go`, add (near `runTailscale`):

```go
func runPair(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	return lifecycle.Pair(cmd.Context(), record.IP, record.User)
}
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./cmd/... -run TestLookupCommands_NameFlagResolves -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/lifecycle/pair.go internal/lifecycle/pair_test.go cmd/lookup.go cmd/lookup_run.go cmd/lookup_test.go
git commit -m "Add cloudlab pair command"
```

---

### Task 10: `cloudlab secrets` command group

**Files:**
- Create: `cmd/secrets.go`
- Create: `cmd/secrets_test.go`
- Modify: `cmd/root.go`

**Interfaces:**
- Consumes: `secrets.Path`/`Init`/`Keys`/`Edit` (Tasks 2-3).

- [ ] **Step 1: Write the failing tests**

Create `cmd/secrets_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/... -run TestSecrets -v`
Expected: FAIL (`secrets` isn't a registered command yet).

- [ ] **Step 3: Write the implementation**

Create `cmd/secrets.go`:

```go
package cmd

import (
	"fmt"

	"github.com/jskswamy/cloudlab/internal/secrets"
	"github.com/spf13/cobra"
)

// newSecretsCmd builds the `cloudlab secrets` command group. Unlike
// every lookupCommandSpecs entry, these never touch an instance or
// state.Record -- secrets.yaml is purely local to the developer's
// machine.
func newSecretsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "secrets",
		Short: "Manage cloudlab's personal, sops-encrypted secrets file",
	}
	root.AddCommand(newSecretsInitCmd(), newSecretsEditCmd(), newSecretsKeysCmd())
	return root
}

func newSecretsInitCmd() *cobra.Command {
	var recipients []string
	c := &cobra.Command{
		Use:   "init",
		Short: "Create a fresh, empty secrets file encrypted for the given age recipient(s)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := secrets.Path()
			if err != nil {
				return err
			}
			if err := secrets.Init(cmd.Context(), path, recipients); err != nil {
				return err
			}
			cmd.Printf("Created %s\n", path)
			return nil
		},
	}
	c.Flags().StringArrayVar(&recipients, "age", nil, "age public key or YubiKey-plugin recipient to encrypt for (repeatable)")
	return c
}

func newSecretsEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the secrets file in $EDITOR via sops (decrypts, edits, re-encrypts)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := secrets.Path()
			if err != nil {
				return err
			}
			return secrets.Edit(cmd.Context(), path)
		},
	}
}

func newSecretsKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "List key names in the secrets file (never values)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := secrets.Path()
			if err != nil {
				return err
			}
			keys, err := secrets.Keys(path)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "no keys")
				return err
			}
			for _, k := range keys {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), k); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
```

In `cmd/root.go`, register it alongside the other top-level commands:

```go
	root.AddCommand(newListCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newProvisionCmd())
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newLookupCommands()...)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/secrets.go cmd/secrets_test.go cmd/root.go
git commit -m "Add cloudlab secrets init/edit/keys commands"
```

---

### Task 11: Update architecture docs

**Files:**
- Modify: `docs/architecture.md`

**Interfaces:**
- None — documentation only.

- [ ] **Step 1: Add the three new commands to the Command surface table**

In `docs/architecture.md`'s Command surface table, add rows after `tmux` and after `down` respectively:

```markdown
| `tailscale [name]` | per-instance | Join the instance to your personal Tailscale network (optional, gated by `cloudlab.pkl`'s `tailscale` field; also runs automatically as part of `up` when that field is `true`) |
| `pair [name]` | per-instance | Pair the getmoshi.app mobile app with the instance via its Easy Pair QR flow (`moshi-hook host setup`) |
```

and, in the global section (alongside `list`):

```markdown
| `secrets init/edit/keys` | global | Manage cloudlab's personal, sops-encrypted secrets file (Tailscale auth key today) |
```

- [ ] **Step 2: Run the full test suite as a final sanity check**

Run: `nix develop --command go test ./...`
Expected: PASS across every package.

- [ ] **Step 3: Commit**

```bash
git add docs/architecture.md
git commit -m "Document tailscale/pair/secrets in the command surface table"
```
