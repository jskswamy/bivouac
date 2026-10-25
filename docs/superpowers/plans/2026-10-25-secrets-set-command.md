# `bivouac secrets set` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `bivouac secrets set <key> [value] [flags]` to securely store and encrypt secrets (tokens, JSON keys, PEM certs) in `~/.config/bivouac/secrets.yaml` via SOPS.

**Architecture:** Add a core `secrets.Set(ctx, path, key, value)` function in `internal/secrets` that writes values as JSON string scalars using `sops set --value-file` with a short-lived mode `0600` temp file (preserving `cmd.Stdin = os.Stdin` for YubiKey hardware PIN prompts). Wire the Cobra CLI subcommand `bivouac secrets set` with support for positional arguments, `--file`, `--editor`, stdin pipes, interactive terminal prompting, overwrite confirmation, and auto-initialization.

**Tech Stack:** Go, Cobra CLI, SOPS, age, `golang.org/x/term`.

**Spec:** [`docs/superpowers/specs/2026-10-25-secrets-set-command-design.md`](file:///Users/subramk/source/github.com/jskswamy/bivouac/docs/superpowers/specs/2026-10-25-secrets-set-command-design.md)

## Global Constraints

- Never expose secret contents in process argv / `ps` (use `sops set --value-file <tempfile>`).
- Preserve `cmd.Stdin = os.Stdin` and call `provider.SuspendTerminal(ctx)` so hardware YubiKey PIN prompts reach the terminal.
- Validate key name syntax against `^[A-Za-z0-9_]+$` and reject `"sops"`.
- `secrets.Decrypt`'s existing contract trims trailing newlines; `secrets.Set` stores input as a string scalar without mutating `Decrypt`.
- Classic commit style: imperative subject under 50 chars, no period, body wrapped at 72 chars, NO AI co-author trailers.

## Review Focus

- Empty secret value: when a user provides an empty string or empty file, error clearly instead of storing an empty or broken entry.
- File not found: when `--file` points to a non-existent path, error immediately before prompting or modifying secrets.yaml.
- Key named "sops": explicitly reject setting the reserved SOPS metadata key.
- Key with invalid characters (e.g. `gcp-key`, `my.key`): reject with descriptive validation message matching `^[A-Za-z0-9_]+$`.
- Overwrite in non-interactive mode without `--force`: fail with exit code and message directing user to `--force`.

---

### Task 1: Core `secrets.Set` Function in `internal/secrets`

**Files:**
- Modify: `internal/secrets/secrets.go`
- Test: `internal/secrets/secrets_test.go`

**Interfaces:**
- Produces: `func Set(ctx context.Context, path, key string, value []byte) error`
- Consumes: `tool.Require("sops")`, `provider.SuspendTerminal(ctx)`, `topLevelKeyPattern`

- [ ] **Step 1: Write failing tests in `internal/secrets/secrets_test.go`**

```go
func TestSet_NewKeyAndDecrypt(t *testing.T)
func TestSet_OverwriteExistingKey(t *testing.T)
func TestSet_PreservesMultilineAndJSON(t *testing.T)
func TestSet_RejectsInvalidKeyNames(t *testing.T)
func TestSet_FailsWhenSecretsFileNotFound(t *testing.T)
```

Test setting a single token, setting multi-line JSON (like GCP service account JSON), verifying `secrets.Decrypt` returns the payload, overwriting an existing key, and validating key rejection on `foo-bar`, `foo.bar`, ``, and `sops`.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test -v ./internal/secrets -run TestSet_ > /tmp/set_test.log 2>&1; cat /tmp/set_test.log`
Expected: FAIL with `undefined: secrets.Set`

- [ ] **Step 3: Implement `secrets.Set` in `internal/secrets/secrets.go`**

1. Validate key:
   - Check `key != "sops"` and `topLevelKeyPattern.MatchString(key + ":")`.
   - If invalid, return `fmt.Errorf("invalid secret key %q: keys must match ^[A-Za-z0-9_]+$ and cannot be %q", key, "sops")`.
2. Check `tool.Require("sops")`.
3. Check `os.Stat(path)`; if not exist, return error directing to `secrets.Init`.
4. Create temporary directory with `0700` and temporary file with `0600`.
5. JSON-encode `string(value)` (`json.Marshal(string(value))`) and write to temp file.
6. Execute `sops set --value-file <path> '["<key>"]' <tempfile>`.
   - Set `cmd.Stdin = os.Stdin`.
   - Wrap with `resume := provider.SuspendTerminal(ctx); defer resume()`.
7. Unlink temp file in `defer`.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test -v ./internal/secrets -run TestSet_ > /tmp/set_test.log 2>&1; cat /tmp/set_test.log`
Expected: PASS

- [ ] **Step 5: Run full package tests**

Run: `go test ./internal/secrets > /tmp/secrets_all.log 2>&1; cat /tmp/secrets_all.log`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/secrets/secrets.go internal/secrets/secrets_test.go
git commit -m "Add secrets.Set to store encrypted string values"
```

---

### Task 2: CLI Command `bivouac secrets set` in `cmd/secrets.go`

**Files:**
- Modify: `cmd/secrets.go`
- Test: `cmd/secrets_test.go`

**Interfaces:**
- Consumes: `secrets.Set`, `secrets.Keys`, `secrets.Init`, `secrets.Path`
- Produces: `bivouac secrets set <key> [value] [flags]`

- [ ] **Step 1: Write CLI unit tests in `cmd/secrets_test.go`**

```go
func TestSecretsSet_PositionalValue(t *testing.T)
func TestSecretsSet_FileFlag(t *testing.T)
func TestSecretsSet_StdinPipe(t *testing.T)
func TestSecretsSet_OverwriteRefusedWithoutForce(t *testing.T)
func TestSecretsSet_OverwriteAllowedWithForce(t *testing.T)
func TestSecretsSet_AutoInitWithAgeFlag(t *testing.T)
func TestSecretsSet_RejectsInvalidKey(t *testing.T)
```

- [ ] **Step 2: Run CLI tests to verify failure**

Run: `go test -v ./cmd -run TestSecretsSet_ > /tmp/cmd_set_test.log 2>&1; cat /tmp/cmd_set_test.log`
Expected: FAIL with unknown command "set"

- [ ] **Step 3: Implement `newSecretsSetCmd` in `cmd/secrets.go`**

1. Register flags:
   - `-f, --file string`
   - `-e, --editor bool`
   - `--force bool`
   - `--age []string`
2. Positional validation: minimum 1 arg (`<key>`), maximum 2 args (`<key> [value]`).
3. Auto-init logic if `secrets.Path()` does not exist:
   - If `--age` provided, call `secrets.Init`.
   - If interactive, prompt for age recipient and call `secrets.Init`.
   - Else return error.
4. Input value resolution:
   - Positional arg (if present and != `-`).
   - `--file` (read file or stdin if `-`).
   - `--editor` (launch `$EDITOR` on a secure temp file via `exec.CommandContext`).
   - Stdin pipe if `!term.IsTerminal`.
   - Interactive prompt if terminal: paste with `Ctrl+D` or press Enter to launch editor.
5. Overwrite confirmation:
   - Check `secrets.Keys(path)`.
   - If exists and not `--force`: prompt if interactive, fail if non-interactive.
6. Call `secrets.Set(ctx, path, key, val)`.
7. Print `Set secret "<key>" in <path>` on `cmd.OutOrStdout()`.
8. Register `newSecretsSetCmd()` in `newSecretsCmd()`.

- [ ] **Step 4: Run CLI tests to verify pass**

Run: `go test -v ./cmd -run TestSecretsSet_ > /tmp/cmd_set_test.log 2>&1; cat /tmp/cmd_set_test.log`
Expected: PASS

- [ ] **Step 5: Run all Go unit tests and linter**

Run: `go test ./... > /tmp/test_all.log 2>&1 && pre-commit run --all-files`
Expected: PASS with 0 lint errors

- [ ] **Step 6: Commit**

```bash
git add cmd/secrets.go cmd/secrets_test.go
git commit -m "Add bivouac secrets set command"
```

---

### Task 3: Documentation and Release Notes

**Files:**
- Modify: `docs/gcloud.md`
- Modify: `RELEASE_NOTES.md`

- [ ] **Step 1: Update `docs/gcloud.md`**

Replace manual `bivouac secrets edit` instructions for GCP service account keys with:
```bash
bivouac secrets set gcloud_service_account_key --file ~/bivouac-gcp-key.json
```

- [ ] **Step 2: Add entry in `RELEASE_NOTES.md`**

Add an entry under `## Unreleased`:
- `bivouac secrets set <key>` command to store encrypted credentials from arguments, files (`--file`), `$EDITOR` (`--editor`), standard input, or interactive prompts.

- [ ] **Step 3: Verify formatting and linter**

Run: `pre-commit run --all-files`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add docs/gcloud.md RELEASE_NOTES.md
git commit -m "Document bivouac secrets set command"
```
