# gh CLI and an optional GitHub token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Install `gh` unconditionally on every instance, and — only when the
user has put a `github_token` in their personal secrets file — configure it
with a token so it isn't limited to GitHub's 60-requests/hour anonymous rate
limit.

**Architecture:** One line in the shared home-manager module installs `gh`.
A new `placeGitHubToken` function, modeled directly on the existing
`placeDoltCredential`/`JoinTailscale` functions, decrypts an optional secret
locally, ships it over the already-open SSH connection during `Reconcile`,
and writes it into `gh`'s own config file inside a tmpfs directory symlinked
from `~/.config/gh` — never into an environment variable, and never onto the
instance's persistent disk.

**Tech Stack:** Go (cobra CLI, `golang.org/x/crypto/ssh`), Nix/home-manager,
`sops` + `age` for the local secrets file, `gh`'s native `hosts.yml` format.

## Global Constraints

- No new `cloudlab.pkl` field. The token is gated purely on whether
  `github_token` decrypts successfully from the user's sops secrets file.
- The plaintext token must never touch the instance's persistent disk, an
  environment variable, or a command-line argument — only `WriteSecretFile`
  over SSH stdin into a tmpfs path, exactly like `tailscale_authkey` and
  `dolthub_creds` today.
- Never overwrite something already at `~/.config/gh` that isn't cloudlab's
  own symlink (e.g. a real `gh auth login`) — warn and skip instead.
- A missing or absent secret is never a warning-worthy failure: `gh` simply
  stays unauthenticated. Only a foreign `~/.config/gh` gets `ReportWarning`.
- Full spec: `docs/superpowers/specs/2026-09-14-gh-cli-and-github-token-design.md`.

---

## File Structure

- **`templates/modules/common.nix`** — add `pkgs.gh` to the existing
  `config.home.packages` list. No new module, no new option.
- **`internal/reconcile/dolt.go`** — `doltPrepareScript` becomes a thin
  wrapper around a new, generalized `symlinkPrepareScript(linkName,
  targetDir string) string`, so `github.go` can reuse the same shell logic
  instead of duplicating it.
- **`internal/reconcile/github.go`** (new) — `placeGitHubToken(ctx, client
  *Client)`, following `placeDoltCredential`'s shape: decrypt, resolve
  `$XDG_RUNTIME_DIR`, prepare the symlink, write the config file.
- **`internal/reconcile/github_test.go`** (new) — mirrors
  `internal/reconcile/dolt_test.go`'s and
  `internal/lifecycle/tailscale_test.go`'s existing test shapes.
- **`internal/reconcile/reconcile.go`** — one added call to
  `placeGitHubToken`, next to the existing `placeDoltCredential` call at the
  end of `Reconcile`.
- **`docs/config.md`** — a new `### GitHub` section styled on the existing
  `### beads` section, plus adding `gh` to the "shared module every instance
  gets" package list.

---

## Task 1: Extract `symlinkPrepareScript` from `doltPrepareScript`

**Files:**
- Modify: `internal/reconcile/dolt.go:49-74` (the `doltPrepareScript`
  function and its doc comment)
- Test: `internal/reconcile/dolt_test.go` (existing
  `TestDoltPrepareScript_LeavesAnythingThatIsNotOurOwnSymlinkAlone` must
  keep passing unchanged — this task is a pure refactor, not a behavior
  change)

**Interfaces:**
- Produces: `symlinkPrepareScript(linkName, targetDir string) string` —
  `linkName` is a dotfile name relative to `$HOME` (e.g. `.dolt`,
  `.config/gh`), `targetDir` is the absolute tmpfs directory it should point
  at. Task 2 and Task 3 call this directly.
- `doltPrepareScript(doltDir string) string` keeps its existing signature
  and behavior (`internal/reconcile/reconcile.go` calls it unchanged), but
  becomes a one-line wrapper.

This is a **refactor task with no new behavior** — the existing dolt test
suite is the regression guard. There is no new test to write; the steps
below are "make the change, then prove nothing broke."

- [ ] **Step 1: Run the existing dolt tests to confirm the baseline passes**

Run: `go test ./internal/reconcile/... -run TestDoltPrepareScript -v`
Expected: PASS (6 subtests, all green) — this is the safety net for the
refactor that follows.

- [ ] **Step 2: Extract the generalized function**

Replace `doltPrepareScript` in `internal/reconcile/dolt.go` (currently
lines 63-74) with:

```go
// symlinkPrepareScript builds a shell script that points linkName (a
// dotfile path relative to $HOME, e.g. ".dolt" or ".config/gh") at
// targetDir, without disturbing anything already there that isn't
// cloudlab's own symlink.
//
// -L is checked before -e so a dangling symlink (target gone, -e false) is
// still recognized as a symlink rather than falling through to the -e
// branch and being reported as "nothing there at all". A symlink already
// pointing at targetDir is left alone (and re-linked, harmlessly, since
// ln -sfn is idempotent); a symlink pointing anywhere else, live or
// dangling, is foreign and reported via NOTASYMLINK exactly like a real
// directory or a regular file -- none of those are cloudlab's to touch.
func symlinkPrepareScript(linkName, targetDir string) string {
	link := "\"$HOME/" + linkName + "\""
	target := shellcmd.Quote(targetDir)
	guard := "if [ -L " + link + " ]; then" +
		" [ \"$(readlink " + link + ")\" = " + target + " ] || { echo NOTASYMLINK; exit 0; }" +
		"; elif [ -e " + link + " ]; then echo NOTASYMLINK; exit 0" +
		"; fi"
	return "set -e" +
		"; mkdir -p " + shellcmd.Quote(targetDir) +
		"; chmod 700 " + shellcmd.Quote(targetDir) +
		"; " + guard +
		"; ln -sfn " + target + " " + link
}

// doltPrepareScript prepares $HOME/.dolt to point at doltDir. See
// symlinkPrepareScript for the mechanics.
func doltPrepareScript(doltDir string) string {
	return symlinkPrepareScript(".dolt", doltDir)
}
```

Note the one behavior-preserving wrinkle: the original
`doltPrepareScript` created `doltDir+"/creds"` (the dolt-specific
subdirectory), not `doltDir` alone. Keep that in the wrapper rather than
losing it in the generalization:

```go
func doltPrepareScript(doltDir string) string {
	return "mkdir -p " + shellcmd.Quote(doltDir+"/creds") + "; " +
		symlinkPrepareScript(".dolt", doltDir)
}
```

(`symlinkPrepareScript` itself still does its own `mkdir -p targetDir` —
that's fine, both are idempotent; the dolt wrapper only adds the `creds`
subdirectory on top.)

- [ ] **Step 3: Run the dolt tests again to confirm the refactor is behavior-preserving**

Run: `go test ./internal/reconcile/... -run TestDoltPrepareScript -v`
Expected: PASS, identical to Step 1's output.

- [ ] **Step 4: Run the full reconcile package test suite**

Run: `go test ./internal/reconcile/... -v`
Expected: PASS — this catches any other dolt test that depends on
`doltPrepareScript`'s exact output.

- [ ] **Step 5: Commit**

```bash
git add internal/reconcile/dolt.go
git commit -m "Share the symlink-prepare script dolt and gh both need"
```

---

## Task 2: `placeGitHubToken` — the two skip paths

**Files:**
- Create: `internal/reconcile/github.go`
- Create: `internal/reconcile/github_test.go`

**Interfaces:**
- Consumes: `secrets.Decrypt(ctx, path, key string) ([]byte, error)` and
  `secrets.Zero(b []byte)` from `internal/secrets` (already used by
  `dolt.go`); `secrets.Path() (string, error)`; `provider.ReportProgress`
  and `provider.ReportWarning` from `internal/provider`; `*Client` (this
  package) with its existing `Run(cmd string) (string, error)` and
  `WriteSecretFile(remotePath string, content []byte) error` methods;
  `symlinkPrepareScript` from Task 1; `shellcmd.LoginShell(cmd string)
  string` from `internal/shellcmd`.
- Produces: `func placeGitHubToken(ctx context.Context, client *Client)` —
  called by Task 4. No return value, same as `placeDoltCredential` — a
  missing optional secret must never fail `Reconcile`.

This task covers the two paths that never reach a real write: secret
missing, and (partially) proving the function's shape. The full write-path
test is Task 3, once the write logic itself exists — but TDD here still
means writing the *first* failing test before any implementation, so start
with the skip case.

- [ ] **Step 1: Write the failing test for "no secret configured"**

Create `internal/reconcile/github_test.go`:

```go
package reconcile

import (
	"bytes"
	"context"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/testenv"
)

func TestPlaceGitHubToken_SkipsCleanlyWhenNoSecretIsConfigured(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// XDG_CONFIG_HOME at an empty temp dir means secrets.Path() names a
	// file that does not exist -- the ordinary "never ran secrets init,
	// or never added this key" case.
	testenv.Isolate(t)

	// A nil client would panic if anything ran -- proving this never
	// tries to connect is the point of the test.
	placeGitHubToken(ctx, nil)

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence: a missing optional secret is not a warning", errOut.String())
	}
	got := out.String()
	if got == "" {
		t.Fatal("out is empty, want an informational note that gh will run unauthenticated")
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/reconcile/... -run TestPlaceGitHubToken_SkipsCleanlyWhenNoSecretIsConfigured -v`
Expected: FAIL — `placeGitHubToken` is undefined.

- [ ] **Step 3: Write the minimal implementation to pass Step 2**

Create `internal/reconcile/github.go`:

```go
package reconcile

import (
	"context"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/secrets"
	"github.com/jskswamy/cloudlab/internal/shellcmd"
)

// placeGitHubToken ships the user's GitHub token to the instance, so gh
// there can authenticate instead of running against GitHub's 60
// requests/hour anonymous rate limit.
//
// Purely optional: gated on whether github_token decrypts successfully,
// with no cloudlab.pkl field to enable or disable it. A missing secrets
// file, or one with no github_token key, is not a warning -- unlike
// placeDoltCredential's missing-credential case, gh has a fully working
// fallback (unauthenticated, just rate-limited) rather than a config-
// declared feature it cannot deliver on.
//
// Like placeDoltCredential and JoinTailscale, this writes only into
// $XDG_RUNTIME_DIR (tmpfs): the plaintext never touches the instance's
// persistent disk, and a reboot's recovery is `cloudlab provision`,
// already idempotent.
func placeGitHubToken(ctx context.Context, client *Client) {
	path, err := secrets.Path()
	if err != nil {
		provider.ReportProgress(ctx, "gh: no secrets file ("+err.Error()+"); gh will run unauthenticated (60 req/hour, fine for public repos)")
		return
	}
	token, err := secrets.Decrypt(ctx, path, "github_token")
	if err != nil {
		provider.ReportProgress(ctx, "gh: no github_token in "+path+"; gh will run unauthenticated (60 req/hour, fine for public repos)")
		return
	}
	defer secrets.Zero(token)

	placeGitHubTokenValue(ctx, client, token)
}

// placeGitHubTokenValue is placeGitHubToken once a token has been
// decrypted. Split out so Task 3's write-path test can drive it directly
// without needing a real sops-encrypted fixture for every case.
func placeGitHubTokenValue(ctx context.Context, client *Client, token []byte) {
	runtimeDir, err := client.Run(shellcmd.LoginShell(`printf '%s' "$XDG_RUNTIME_DIR"`))
	if err != nil {
		provider.ReportWarning(ctx, "gh: could not resolve the instance's runtime directory: "+err.Error()+"; gh will run unauthenticated")
		return
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		provider.ReportWarning(ctx, "gh: the instance has no $XDG_RUNTIME_DIR; gh will run unauthenticated")
		return
	}

	ghDir := runtimeDir + "/cloudlab/gh"
	out, err := client.Run(shellcmd.LoginShell(symlinkPrepareScript(".config/gh", ghDir)))
	if err != nil {
		provider.ReportWarning(ctx, "gh: could not prepare ~/.config/gh on the instance: "+err.Error()+"\n"+out)
		return
	}
	if strings.Contains(out, "NOTASYMLINK") {
		provider.ReportWarning(ctx, "gh: ~/.config/gh on the instance already exists and is not cloudlab's symlink — leaving it alone; gh will run unauthenticated")
		return
	}

	hostsYAML := "github.com:\n    oauth_token: " + string(token) + "\n    git_protocol: https\n"
	if err := client.WriteSecretFile(ghDir+"/hosts.yml", []byte(hostsYAML)); err != nil {
		provider.ReportWarning(ctx, "gh: writing the GitHub token: "+err.Error())
	}
}
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `go test ./internal/reconcile/... -run TestPlaceGitHubToken_SkipsCleanlyWhenNoSecretIsConfigured -v`
Expected: PASS

- [ ] **Step 5: Write the failing test for "foreign ~/.config/gh"**

Add to `internal/reconcile/github_test.go`:

```go
func TestPlaceGitHubTokenValue_LeavesAForeignConfigGhAlone(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		if strings.Contains(cmd, "XDG_RUNTIME_DIR") {
			return "/run/user/1000", 0
		}
		if strings.Contains(cmd, "config/gh") {
			return "NOTASYMLINK\n", 0
		}
		return "", 0
	})
	startFakeAgent(t)
	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	placeGitHubTokenValue(ctx, client, []byte("ghp_example"))

	got := errOut.String()
	if !strings.Contains(got, "not cloudlab's symlink") {
		t.Errorf("errOut = %q, want it to explain ~/.config/gh is foreign", got)
	}
	for _, cmd := range commands {
		if strings.Contains(cmd, "install") || strings.Contains(cmd, "hosts.yml") {
			t.Errorf("commands = %v, want no write attempted once NOTASYMLINK is seen", commands)
		}
	}
}
```

Add `"strings"` to the test file's imports if not already present from
Step 1 (it is not, in the minimal Step-1 version — add it now).

- [ ] **Step 6: Run it to confirm it fails**

Run: `go test ./internal/reconcile/... -run TestPlaceGitHubTokenValue_LeavesAForeignConfigGhAlone -v`
Expected: This may actually PASS already if Step 3's implementation is
correct — that's fine and expected for a case the implementation already
handles. If it fails, the failure will point at whatever's missing (e.g. a
typo in the `NOTASYMLINK` check); fix `github.go` until it passes.

- [ ] **Step 7: Confirm it passes**

Run: `go test ./internal/reconcile/... -run TestPlaceGitHubTokenValue_LeavesAForeignConfigGhAlone -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/reconcile/github.go internal/reconcile/github_test.go
git commit -m "Skip gh token placement when absent or the config dir is foreign"
```

---

## Task 3: `placeGitHubToken` — the success path

**Files:**
- Modify: `internal/reconcile/github_test.go` (add the end-to-end test)
- No production code change expected — Task 2's `placeGitHubTokenValue`
  should already implement the success path correctly. This task exists to
  *prove* that with a real fake SSH server and a real decrypted secret, and
  to fix anything Task 2 got wrong now that it's exercised end-to-end.

**Interfaces:**
- Consumes: `startFakeSSHServer(t, handler) string` and `startFakeAgent(t)`
  from `internal/reconcile/ssh_test.go` (already used by `reconcile_test.go`
  and `ssh_test.go` in this package); the `writeTailscaleSecretsFixture`
  pattern from `internal/lifecycle/tailscale_test.go` (duplicated here, not
  imported — see that file's own comment on why this boilerplate is
  duplicated per package rather than shared).

- [ ] **Step 1: Write the failing end-to-end test**

Add to `internal/reconcile/github_test.go`:

```go
// writeGitHubTokenSecretsFixture generates a fresh age identity, points
// SOPS_AGE_KEY_FILE and XDG_CONFIG_HOME at temp locations, and writes a
// real sops-encrypted secrets.yaml containing github_token -- exactly
// what placeGitHubToken decrypts in production. Duplicated from
// internal/lifecycle/tailscale_test.go's writeTailscaleSecretsFixture
// rather than shared -- see that function's own comment for why.
func writeGitHubTokenSecretsFixture(t *testing.T, token string) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "age-key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v\n%s", out, err)
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
	testenv.Isolate(t)

	plainPath := filepath.Join(t.TempDir(), "plain.yaml")
	if err := os.WriteFile(plainPath, []byte("github_token: "+token+"\n"), 0o600); err != nil {
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

func TestPlaceGitHubToken_WritesHostsYAMLWhenTokenIsConfigured(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)
	writeGitHubTokenSecretsFixture(t, "ghp_exampletoken123")

	var commands []string
	var stdins [][]byte
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		stdins = append(stdins, stdin)
		if strings.Contains(cmd, "XDG_RUNTIME_DIR") {
			return "/run/user/1000", 0
		}
		return "", 0
	})
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	placeGitHubToken(ctx, client)

	if errOut.Len() != 0 {
		t.Fatalf("errOut = %q, want no warnings on a clean write", errOut.String())
	}
	if len(commands) != 3 {
		t.Fatalf("commands = %v, want 3 (resolve runtime dir, prepare symlink, write hosts.yml)", commands)
	}
	if !strings.Contains(commands[1], "config/gh") {
		t.Errorf("commands[1] = %q, want it to prepare ~/.config/gh", commands[1])
	}
	wantYAML := "github.com:\n    oauth_token: ghp_exampletoken123\n    git_protocol: https\n"
	if string(stdins[2]) != wantYAML {
		t.Errorf("stdin to the write step = %q, want %q", stdins[2], wantYAML)
	}
	if !strings.Contains(commands[2], "/run/user/1000/cloudlab/gh/hosts.yml") {
		t.Errorf("commands[2] = %q, want it to write into the resolved runtime dir", commands[2])
	}
	for i, cmd := range commands {
		if strings.Contains(cmd, "ghp_exampletoken123") {
			t.Errorf("commands[%d] = %q contains the token literal -- it must only travel via stdin", i, cmd)
		}
	}
}
```

Add `"os"`, `"os/exec"`, `"path/filepath"`, and
`"github.com/jskswamy/cloudlab/internal/secrets"` to the test file's
imports.

- [ ] **Step 2: Run it to see where it actually stands**

Run: `go test ./internal/reconcile/... -run TestPlaceGitHubToken_WritesHostsYAMLWhenTokenIsConfigured -v`
Expected: Likely PASS already, since Task 2 implemented the full write
path — this test's job is to catch anything that doesn't survive contact
with a real fake SSH round-trip (quoting bugs, wrong path joins, wrong
YAML indentation). If it fails, fix `internal/reconcile/github.go` — most
likely causes: a shell-quoting mismatch in `ghDir+"/hosts.yml"`, or the
YAML string not matching `gh`'s expected two-space-then-four-space nesting.

- [ ] **Step 3: Confirm it passes**

Run: `go test ./internal/reconcile/... -run TestPlaceGitHubToken -v`
Expected: PASS, all three `TestPlaceGitHubToken*` tests green.

- [ ] **Step 4: Run the full package suite once more**

Run: `go test ./internal/reconcile/... -v`
Expected: PASS, nothing else disturbed.

- [ ] **Step 5: Commit**

```bash
git add internal/reconcile/github_test.go
git commit -m "Prove gh token placement end to end against a fake instance"
```

---

## Task 4: Wire `placeGitHubToken` into `Reconcile`

**Files:**
- Modify: `internal/reconcile/reconcile.go:125-130`

**Interfaces:**
- Consumes: `placeGitHubToken(ctx context.Context, client *Client)` from
  Task 2/3.

- [ ] **Step 1: Add the call**

In `internal/reconcile/reconcile.go`, `Reconcile`'s final lines currently
read:

```go
	placeDoltCredential(ctx, client, config.BeadsMode(cfg.Beads), filepath.Dir(cloudlabPath))
	return nil
}
```

Change to:

```go
	placeDoltCredential(ctx, client, config.BeadsMode(cfg.Beads), filepath.Dir(cloudlabPath))
	placeGitHubToken(ctx, client)
	return nil
}
```

- [ ] **Step 2: Run the full reconcile test suite**

Run: `go test ./internal/reconcile/... -v`
Expected: PASS. `Reconcile`'s own tests (`reconcile_test.go`) use
`startFakeSSHServer` handlers that return `("", 0)` for any command they
don't explicitly match, so the added `placeGitHubToken` call will hit
`secrets.Path()`/`secrets.Decrypt` and skip cleanly in every existing
`Reconcile` test (none of them call `testenv.Isolate` with a
`github_token` secret present) — this should not require touching any
existing `reconcile_test.go` assertions. If any existing test's exact
command-count assertion breaks, that test was counting SSH round-trips
before `placeGitHubToken` existed and needs no code fix — the test itself
needs the fake handler count updated only if `placeGitHubToken` now makes
a remote call (it does not, if the secrets file is absent, which every
`Reconcile` test currently leaves unset).

- [ ] **Step 3: Build the whole module**

Run: `go build ./...`
Expected: succeeds with no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/reconcile/reconcile.go
git commit -m "Ship the optional GitHub token on every reconcile"
```

---

## Task 5: Install `gh` unconditionally

**Files:**
- Modify: `templates/modules/common.nix:54-73` (`config.home.packages`)

**Interfaces:** None — this is a standalone Nix change with no Go
interface.

- [ ] **Step 1: Add the package**

In `templates/modules/common.nix`, `config.home.packages` currently starts:

```nix
  config.home.packages = [
    pkgs.git
    pkgs.age
    beads
```

Add `pkgs.gh` next to `pkgs.git`:

```nix
  config.home.packages = [
    pkgs.git
    pkgs.gh
    pkgs.age
    beads
```

- [ ] **Step 2: Validate the flake evaluates**

Run: `nix flake check ./templates 2>&1 | tail -40`

If this project's CI runs a different validation command, check
`.github/workflows/` for the exact invocation used there (see
`docs/superpowers/specs/2026-09-01-github-actions-ci-design.md`) and run
that instead — the point is confirming `common.nix` still evaluates with
`gh` added, since there is no Go test that covers this file.

Expected: no evaluation errors. `pkgs.gh` is a standard nixpkgs attribute,
so this should succeed without a lockfile update.

- [ ] **Step 3: Commit**

```bash
git add templates/modules/common.nix
git commit -m "Install gh on every instance"
```

---

## Task 6: Document `github_token`

**Files:**
- Modify: `docs/config.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Add `gh` to the shared-module package list**

`docs/config.md` around line 145-151 currently reads:

```markdown
### Templates

`"python"` gives you `python312` and `uv`; `"docker"` gives you `docker` and
`minikube`, with the daemon and group membership already wired up. Both
build on a shared module every instance gets — `git`, `age`, `devbox`,
`herdr`, `mosh`, `moshi-hook`, `tailscale`, `tmux` (preconfigured), plus
`fish` and `starship`.
```

Change the package list to include `gh`:

```markdown
build on a shared module every instance gets — `git`, `gh`, `age`, `devbox`,
`herdr`, `mosh`, `moshi-hook`, `tailscale`, `tmux` (preconfigured), plus
`fish` and `starship`.
```

- [ ] **Step 2: Add a `### GitHub` section**

Add a new section after the existing `### beads` section (which ends
around line 143, just before `### Templates`) in `docs/config.md`:

```markdown
### GitHub

`gh` is installed on every instance and works unauthenticated out of the
box — fine for public repos, subject to GitHub's 60-requests/hour anonymous
rate limit.

To raise that limit, add `github_token` to your personal secrets file with
`cloudlab secrets edit`:

```yaml
github_token: ghp_…
```

There is no `cloudlab.pkl` field for this — it's purely optional, gated on
whether the secret exists. If it does, `up`/`provision` decrypt it
just-in-time and configure `gh` with it, the same way `tailscale_authkey`
and the DoltHub credentials reach an instance: streamed over SSH into
tmpfs, never written to the instance's persistent disk, never an
environment variable. If it's absent, `gh` is simply left unauthenticated
— not a warning, not a failure.

Like the other tmpfs-only secrets, a reboot clears it; `cloudlab provision`
restores it.
```

- [ ] **Step 3: Proofread against the file it sits beside**

Read the `### beads` section immediately above where this was inserted and
confirm the new section's terminology and tone match (e.g. "tmpfs",
"streamed over SSH", "`cloudlab provision` restores it" are all phrases
this file already uses for the same underlying pattern — the new section
should not invent different wording for the same idea).

- [ ] **Step 4: Commit**

```bash
git add docs/config.md
git commit -m "Document the optional GitHub token"
```

---

## Task 7: Final check

**Files:** None — verification only.

- [ ] **Step 1: Full test suite**

Run: `go test ./... 2>&1 | tail -60`
Expected: PASS everywhere. (If `internal/lifecycle` shows the pre-existing
`TempDir RemoveAll: directory not empty` / SSH-agent-related flakes noted
in this repo's own history around `3d1e2d5 Fail a fake-SSH test that has
not isolated HOME`, that is a known local-environment sensitivity unrelated
to this feature — not a regression to chase down here.)

- [ ] **Step 2: Full build**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 3: Re-read the spec once more**

Re-read `docs/superpowers/specs/2026-09-14-gh-cli-and-github-token-design.md`
top to bottom and confirm every section has a corresponding completed task
above:
- Installing `gh` → Task 5
- The `github_token` secret → Tasks 2, 3
- Shipping it to the instance (including the shared symlink helper) →
  Tasks 1, 2, 3
- `hosts.yml` contents → Task 2/3 (verify the `user:` field question the
  spec flagged: if `gh auth status` or another `gh` subcommand complains
  about its absence when actually exercised against a real instance, add
  it by resolving the username with one extra `gh api user` call before
  shipping the token — not expected to be needed, but the spec explicitly
  left this open)
- Failure/skip behavior → Task 2
- Components touched → all tasks
- Testing → Tasks 1-4

- [ ] **Step 4: Nothing to commit**

This task is verification only — if Step 1 or Step 2 fail, fix the
regression in the task that introduced it and re-run this task from
Step 1.
