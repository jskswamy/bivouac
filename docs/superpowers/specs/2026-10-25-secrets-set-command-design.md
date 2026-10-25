# Provider-Agnostic Secret Storage via `bivouac secrets set`

Status: proposed
Date: 2026-10-25

## Context

Bivouac relies on a personal, sops-encrypted secrets file (`~/.config/bivouac/secrets.yaml`)
for credentials across cloud providers and tools:
- Tailscale auth keys (`tailscale_authkey`)
- DigitalOcean personal access tokens (`digitalocean_token`)
- DoltHub credentials (`dolthub_creds`)
- Google Cloud service account keys (`gcloud_service_account_key`)
- Future provider tokens, custom SSH keys, and certificates

Currently, the CLI only exposes `bivouac secrets init`, `edit`, and `keys`.
To add or update a secret, users must either:
1. Run `bivouac secrets edit` and manually format/paste the secret into `$EDITOR`. For
   complex multi-line values like GCP service account JSON files or private keys, this is
   awkward and prone to YAML indentation mistakes.
2. Directly execute low-level `sops` CLI commands, bypassing bivouac's path resolution
   and UX conventions.
3. Pass secrets via environment variables or unencrypted files on disk.

There is no command to set or import a secret directly from arguments, files, pipes,
or interactive prompts.

## Design

Introduce `bivouac secrets set <key> [value] [flags]` as a generic, cloud-provider-agnostic
command to store and encrypt secrets.

### Command Surface

```text
Usage:
  bivouac secrets set <key> [value] [flags]

Flags:
  -f, --file string         read secret value from file (or "-" for standard input)
  -e, --editor              open $EDITOR to compose or paste secret content
      --force               overwrite existing secret without confirmation
      --age stringArray     age public key recipient(s) for auto-initialization if secrets file does not exist
  -h, --help                help for set
```

### Input Precedence & Resolution

A secret value can be provided in five ways, evaluated in order of precedence:

1. **Positional argument:** `bivouac secrets set <key> <value>`
   - Suitable for short single-line tokens:
     ```bash
     bivouac secrets set digitalocean_token "dop_v1_..."
     ```
   - If the positional value is literally `-`, it directs input to stdin.

2. **File flag (`-f, --file <path>`):**
   - Reads the full file content from `<path>`:
     ```bash
     bivouac secrets set gcloud_service_account_key --file ~/bivouac-gcp-key.json
     ```
   - If `<path>` is `-`, it reads from standard input.

3. **Editor flag (`-e, --editor`):**
   - Opens `$EDITOR` (or `$VISUAL`, defaulting to `nano`/`vim`) on a secure temporary
     buffer (mode `0600`).
   - The user edits or pastes their secret, saves, and exits.
   - Bivouac reads the buffer, unlinks the temporary file, and encrypts the secret.

4. **Standard Input (pipe or redirection):**
   - When no positional value, file flag, or editor flag is given, and standard input is not
     a terminal (`!term.IsTerminal(os.Stdin.Fd())`), reads until EOF:
     ```bash
     cat ~/bivouac-gcp-key.json | bivouac secrets set gcloud_service_account_key
     op read "op://personal/digitalocean/token" | bivouac secrets set digitalocean_token
     ```

5. **Interactive Terminal Prompt:**
   - When no value, flags, or piped input are present and stdin is an interactive terminal:
     Prompts the user:
     ```text
     Paste secret for "<key>" (Ctrl+D to submit, or press Enter to launch $EDITOR):
     ```
   - If the user presses Enter on an empty line without typing anything: launches `$EDITOR`
     flow (same as `--editor`).
   - If the user types or pastes text (token, multi-line JSON, or YAML) and sends `Ctrl+D`:
     reads the entire input directly.

### Key Name Validation

Keys must match the regex `^[A-Za-z0-9_]+$` (alphanumeric and underscores), matching
bivouac's flat top-level mapping convention.
- Rejects empty keys, spaces, dashes, dots, or symbols with a clear error.
- Rejects `sops` as a key name (reserved metadata in SOPS files).

### Value Storage & SOPS Plumbing

All secret values are stored as string scalars in the SOPS YAML file:
- `bivouac secrets set` writes the JSON-encoded value (`json.Marshal(string(rawBytes))`)
  to a short-lived temporary file with `0600` permissions inside a `0700` temporary
  directory.
- Executes `sops set --value-file <secrets-path> '["<key>"]' <temp-file-path>`.
- The temporary file is immediately removed via `defer`.
- **Security & Hardware Key Support:**
  - Using `--value-file` with a short-lived temporary file ensures secret values never appear
    in command-line arguments visible to `ps` or `/proc`.
  - Crucially, this keeps `cmd.Stdin = os.Stdin` available for `age-plugin-yubikey` PIN
    prompts or hardware touch events, which would otherwise fail or intercept secret data
    if stdin were piped to `sops`.
  - Re-uses `provider.SuspendTerminal(ctx)` to release raw terminal mode during execution.
- **Data Integrity:**
  - Storing as a string scalar guarantees that multi-line payloads (such as JSON service
    account keys, PEM certificates, or multi-line configuration blocks) preserve their exact
    content without unintended YAML nesting or restructuring.
  - Top-level keys remain flat, ensuring `secrets.Keys(path)` continues to discover every key
    via its regular expression.
  - `secrets.Decrypt`'s existing contract trims trailing newlines (to normalize YAML block
    scalars); JSON parsers and PEM decoders are unaffected by the trimmed newline.

### Overwrite Confirmation & `--force`

When `<key>` already exists in the secrets file (checked via `secrets.Keys(path)`):
- If `--force` is specified: overwrites the existing value immediately.
- If running in an interactive terminal:
  Prompts the user:
  ```text
  Secret "<key>" already exists in ~/.config/bivouac/secrets.yaml. Overwrite? [y/N]:
  ```
  If the user enters anything other than `y` or `yes`, the command aborts with:
  ```text
  Aborted.
  ```
- If running non-interactively (e.g., CI, automation scripts) and `--force` is not set:
  Fails with exit code 1 and error message:
  ```text
  secret "<key>" already exists in ~/.config/bivouac/secrets.yaml (use --force to overwrite)
  ```

### Automatic Initialization

If `~/.config/bivouac/secrets.yaml` does not exist when `bivouac secrets set` is invoked:
- If `--age` flag(s) are supplied:
  Automatically runs `secrets.Init(ctx, path, recipients)` and creates the file before
  setting the key.
- If running in an interactive terminal and no `--age` flag is provided:
  Prompts the user on the spot:
  ```text
  Secrets file ~/.config/bivouac/secrets.yaml does not exist.
  Enter age recipient public key (e.g. age1...):
  ```
  Initializes the file with the entered recipient, then proceeds with setting the secret.
- If running non-interactively and no `--age` flag is provided:
  Fails with an actionable error:
  ```text
  secrets file ~/.config/bivouac/secrets.yaml doesn't exist yet (run `bivouac secrets init --age <key>` or pass --age)
  ```

### User Feedback

On success, outputs a concise confirmation to stdout without echoing any portion of the secret:
```text
Set secret "gcloud_service_account_key" in ~/.config/bivouac/secrets.yaml
```

## Implementation Structure

1. **`internal/secrets` Package:**
   - `func Set(ctx context.Context, path, key string, value []byte) error`:
     - Checks `tool.Require("sops")`.
     - Validates key matches `^[A-Za-z0-9_]+$` and `key != "sops"`.
     - Writes `json.Marshal(string(value))` to a temporary file (`0600`).
     - Invokes `sops set --value-file <path> '["<key>"]' <temp-file>`.
     - Preserves `cmd.Stdin = os.Stdin` and calls `provider.SuspendTerminal(ctx)`.
   - Unit tests in `internal/secrets/secrets_test.go`:
     - Setting a new key.
     - Overwriting an existing key.
     - Setting complex multi-line JSON (GCP key) and PEM files, verifying `Decrypt` returns the payload correctly.
     - Key name validation (reject spaces, hyphens, "sops").

2. **`cmd/secrets.go` Command:**
   - Add `newSecretsSetCmd()`.
   - Flags: `--file, -f`, `--editor, -e`, `--force`, `--age`.
   - Input resolution ladder:
     1. Positional arg (or `-` for stdin)
     2. `--file` (or `-` for stdin)
     3. `--editor` (launch `$EDITOR` on temp file)
     4. Pipe/redirect stdin (`!term.IsTerminal`)
     5. Interactive prompt (pasting with Ctrl+D, or Enter to launch `$EDITOR`).
   - Existence check via `secrets.Keys(path)`, prompt for overwrite, `--force`.
   - Missing secrets file check, prompt/flag for `--age`, auto-`secrets.Init`.
   - Tests in `cmd/secrets_test.go`.

3. **Documentation & Release Notes:**
   - Update `docs/gcloud.md` with the new command.
   - Update `RELEASE_NOTES.md`.
