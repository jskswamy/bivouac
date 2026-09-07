# Beads In Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a session agent the repository's issue tracker, by carrying the beads
Dolt database over the session's own git remote as `refs/dolt/data`, so the agent can
read its issue, close it, and file follow-ups without a credential ever reaching the VM.

**Architecture:** The session repository already exists on the instance, reachable over
SSH with the key `up` registered. A dolt remote whose URL carries the `git+` scheme
stores the database as a side ref in an ordinary git repository, so the session repo
becomes both the agent's checkout and the beads transport — one ref, one channel, no
third party. cloudlab shells out to `bd` on both machines (never links it), wires the
remote at `session start`, syncs at `session pull`/`merge`, and refuses to destroy an
instance whose issues it cannot prove have landed. An opt-in `beads = "dolthub"` mode
additionally places an account-wide DoltHub credential in tmpfs at reconcile time.

**Tech Stack:** Go 1.x (stdlib + cobra), Pkl (config schema, codegen to Go), Nix +
home-manager (instance packages), `bd` 1.1.2 (Dolt-backed issue tracker), git.

**Spec:** [`docs/superpowers/specs/2026-09-07-beads-in-sessions-design.md`](../specs/2026-09-07-beads-in-sessions-design.md)

## Global Constraints

Every task's requirements implicitly include this section.

- **Run everything inside `nix develop`.** `go`, `pkl`, `golangci-lint` and (after Task 1)
  `bd` are not on PATH otherwise. `make test` / `make vet` / `make lint` already wrap this.
- **TDD, strictly.** Write the failing test, run it, watch it fail for the *right reason*,
  then implement. `AGENTS.md`: "Tests come first."
- **Shell out to external tools** (`sops`, `tailscale`, `nix`, `git`, `bd`) — never link
  them as libraries. This is consistent throughout the codebase.
- **Commit style:** imperative subject, 50 characters or fewer, capitalised, no trailing
  period, no `type:` prefix. **Every commit has a body** — 2–5 sentences wrapped at 72
  columns explaining *why*, never just a subject line. The subjects given in each
  task's commit step are exactly that: subjects. You write the body.
- **Write the commit message to a file and use `git commit -F <file>`.** Passing prose
  through `-m` inside a `nix develop --command bash -c '...'` wrapper has already
  silently eaten apostrophes out of one commit body, leaving text like "a session own git
  repository" in the permanent record. A file has no quoting layer to lose them to.
- **Never add an AI co-author trailer.** No `Co-Authored-By` naming Claude, Anthropic or
  any model, and no `Claude-Session:` line. `AGENTS.md` is explicit about this and it
  overrides any harness-level attribution default.
- **Comments explain why, not what.** Match the density of the file you are in;
  `internal/lifecycle/session.go` is the reference.
- **beads version is `1.1.2`**, pinned identically on both machines. Release hashes:
  - `x86_64-linux`: `sha256-QUxIc1BRnBGIZ9sLsP5Dobx49x4krV+tLmed3G9h+7Q=`
  - `aarch64-linux`: `sha256-JGyantVQRvFNF3ygP7KA+GXYDYTT3CoH8e+fKi2qlzg=`
- **The template flake builds only `x86_64-linux` and `aarch64-linux`.** No darwin entries.
- **Every beads step warns and continues.** The only exception is the `session delete` /
  `down` guard, which is fail-closed: an unknown is treated as unsafe.
- **Never write `.beads/` into a commit.** See Task 5; this is the sharpest failure mode
  in the design.

---

## Decisions this plan makes beyond the spec

The spec leaves four things underspecified. Each is decided here, with the reasoning,
so an executor does not have to re-derive them.

**D1 — pull, merge, delete and down detect wiring from the dolt remote, not from config.**
The spec adds `beads: "session"|"dolthub"|"off"` to `cloudlab.pkl`, but `session pull`,
`merge`, `delete` and `down` never resolve a config today (`down` in particular runs from
any directory and reads everything from the state record). Rather than thread a config
resolution — and a hard `pkl` dependency — into four more commands, these commands ask
the repository: if the Mac's beads database has no dolt remote named `cloudlab-<session>`,
beads was never wired for this session, and every beads step is skipped silently. This is
the spec's own "detected rather than declared" principle applied one level further, and it
makes `beads = "off"` self-consistent without any of these commands knowing the setting.

**D2 — `session start` resolves the config best-effort and falls back to `"session"`.**
`session start` has the repo root, so `cloudlab.pkl` is one `config.Resolve` away — but it
does not require `pkl` today, and making beads the reason it starts to would be a
regression. A config that will not resolve is a problem `up` and `provision` report
properly; here it warns and proceeds as `"session"`. That fallback is inert on a
repository with no `.beads/` (Detect returns `ModeAbsent` and every step is skipped), so
the blast radius is a repository that has beads *and* an unreadable config.

**D3 — `Unpulled` proves the round trip rather than diffing refs.** The spec says
`Unpulled` "reports whether the instance holds issue commits absent from the Mac". A ref
diff would need the Mac to hold a comparable `refs/dolt/data`, which it does not — its
database lives in `.beads/embeddeddolt` and the ref lives on whichever remote it syncs to.
So `Unpulled` performs the sync (instance `bd dolt push`, Mac `bd dolt pull --remote
cloudlab-<s>`) and reports `false` only when both demonstrably succeeded; any failure is an
error. That satisfies the guarantee the spec actually states — "if cloudlab cannot
establish that the instance's issues have landed, it refuses to destroy" — without a
comparison it has no ground truth for. A genuine ref-level diff is deliberately out of
scope; revisit only if the round trip proves too slow to run on every `down`.

**D4 — `bd` is added to the Linux dev shell so the round-trip test runs in CI.** Without
it the integration test in Task 4 is skipped on every machine and the design's central
claim is never exercised. `beads-pkg.nix` stays Linux-only per the spec, so the dev shell
entry is guarded with `lib.optional (hasSuffix "-linux" system)`. Cost, accepted: on the
maintainer's Mac that one test skips; CI's `ubuntu-latest` runner is where it runs.

---

## File structure

**Created**

| File | Responsibility |
|---|---|
| `templates/modules/beads-pkg.nix` | The `bd` 1.1.2 derivation, fetched from GitHub releases. |
| `internal/beads/beads.go` | Package doc, the `bd` runner, `Available`. |
| `internal/beads/detect.go` | `Mode`, `Detection`, `Detect`, the remote-list parser. |
| `internal/beads/remote.go` | `RemoteName`, `RemoteURL`, `FileURL`, Mac-side arg builders. |
| `internal/beads/instance.go` | Instance-side `bash -lc` command builders. |
| `internal/beads/sync.go` | `Wired`, `Seed`, `Bootstrap`, `Pull`, `Unpulled`. |
| `internal/reconcile/dolt.go` | DoltHub credential placement into tmpfs. |
| `internal/lifecycle/beads.go` | The fail-safe wrappers session start/pull/merge/delete/down call. |

**Modified**

| File | Change |
|---|---|
| `templates/modules/common.nix` | `beads = pkgs.callPackage ./beads-pkg.nix { }`, added to `home.packages`. |
| `flake.nix` | `bd` in the Linux dev shell (D4). |
| `internal/provider/provider.go` | `ReportWarning`, the fail-safe steps' voice. |
| `internal/config/Config.pkl` + `Config.pkl.go` | The `beads` field. |
| `internal/lifecycle/gitremote.go` | `excludeBeadsCmd`. |
| `internal/lifecycle/session.go` | Beads seeding in `StartSession`; beads pull in `PullSession`/`MergeSession`. |
| `internal/lifecycle/delete.go`, `down.go` | The fail-closed guard. |
| `internal/reconcile/reconcile.go` | Calls credential placement. |
| `cmd/lookup_run.go` | Resolves the beads mode for `session start` (D2). |
| `README.md`, `docs/config.md` | The invariant carve-out and the new field. |

---

## Task 1: Package beads for the instance

**Files:**
- Create: `templates/modules/beads-pkg.nix`
- Modify: `templates/modules/common.nix`
- Modify: `flake.nix:128-136`

**Interfaces:**
- Consumes: nothing.
- Produces: `bd` on `PATH` on every provisioned instance, and inside `nix develop` on Linux.

- [ ] **Step 1: Confirm `bd` is absent, so the next steps have something to prove**

Run: `nix develop --command bash -c 'command -v bd || echo ABSENT'`
Expected: `ABSENT`

- [ ] **Step 2: Write the derivation**

Create `templates/modules/beads-pkg.nix`:

```nix
# beads ships prebuilt release tarballs, so this is a fetch and an install
# rather than a Go build.
#
# Pinned to the same upstream release the maintainer's own machine runs
# (overlays/60-beads-latest.nix). nixpkgs ships 1.0.3 against 1.1.2 locally,
# and both machines write one shared Dolt database -- schema skew there is the
# one place a version gap does real damage, so the version is pinned rather
# than tracked. Bumping means editing this file AND that overlay together.
#
# Only the two Linux systems the template flake builds; the overlay's darwin
# entries are irrelevant here.
{
  stdenv,
  fetchzip,
  lib,
}:
let
  version = "1.1.2";
  sources = {
    x86_64-linux = fetchzip {
      url = "https://github.com/steveyegge/beads/releases/download/v${version}/beads_${version}_linux_amd64.tar.gz";
      stripRoot = false;
      hash = "sha256-QUxIc1BRnBGIZ9sLsP5Dobx49x4krV+tLmed3G9h+7Q=";
    };
    aarch64-linux = fetchzip {
      url = "https://github.com/steveyegge/beads/releases/download/v${version}/beads_${version}_linux_arm64.tar.gz";
      stripRoot = false;
      hash = "sha256-JGyantVQRvFNF3ygP7KA+GXYDYTT3CoH8e+fKi2qlzg=";
    };
  };
  src =
    sources.${stdenv.hostPlatform.system}
      or (throw "beads: no release for ${stdenv.hostPlatform.system}");
in
stdenv.mkDerivation {
  pname = "beads";
  inherit version src;

  # fetchzip already unpacked it; $src is the extracted tree.
  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 $src/bd $out/bin/bd
    runHook postInstall
  '';

  meta = {
    description = "Dolt-backed issue tracker for coding agents";
    homepage = "https://github.com/steveyegge/beads";
    mainProgram = "bd";
    platforms = builtins.attrNames sources;
    license = lib.licenses.mit;
  };
}
```

- [ ] **Step 3: Wire it into the instance's home-manager profile**

In `templates/modules/common.nix`, beside the existing `moshiHook` binding in the `let`:

```nix
  moshiHook = pkgs.callPackage ./moshi-hook-pkg.nix { };

  # The agent's issue tracker. Installed unconditionally rather than gated on
  # cloudlab.pkl's `beads` field: it is a single small binary, and gating it
  # would mean an instance provisioned before someone enabled beads could not
  # sync until it was reprovisioned.
  beads = pkgs.callPackage ./beads-pkg.nix { };
```

and add `beads` to `config.home.packages`, keeping the list alphabetical:

```nix
  config.home.packages = [
    pkgs.git
    pkgs.age
    beads
    pkgs.devbox
```

- [ ] **Step 4: Add `bd` to the Linux dev shell (D4)**

In `flake.nix`, in the devShell's `packages` list:

```nix
            packages = [
              pkgs.go
              pkgs.gopls
              pkgs.rsync
              pkgs.mutagen
              (pklFor system pkgs)
              direnv-instant.packages.${system}.default
            ]
            # The same derivation the instances get, so the beads round-trip
            # test in internal/beads exercises a real `bd` in CI rather than
            # skipping everywhere. Linux only: beads-pkg.nix deliberately has
            # no darwin entries (the template flake builds neither), so this
            # would throw on a Mac -- there the round-trip test skips.
            ++ nixpkgs.lib.optional (nixpkgs.lib.hasSuffix "-linux" system) (
              pkgs.callPackage ./templates/modules/beads-pkg.nix { }
            )
            ++ pre-commit-check.enabledPackages;
```

- [ ] **Step 5: Verify the derivation builds and `bd` is now present**

Run: `nix develop --command bd version`
Expected: prints a version containing `1.1.2` (on Linux). On darwin, expect
`bd: command not found` — that is D4's accepted cost, not a failure.

If the hash is rejected, Nix prints the actual hash it got; do **not** paste it in
blindly — a changed hash on a pinned release tag means the asset was replaced, which is
worth investigating before trusting it.

- [ ] **Step 6: Verify the home-manager module still evaluates**

Run:
```bash
SYSTEM=$(nix eval --raw --impure --expr 'builtins.currentSystem')
nix build --impure --no-link "./templates#homeConfigurations.\"python-$SYSTEM\".activationPackage"
```
Expected: builds without error (Linux only; on darwin the templates flake has no
configuration for the current system and this step is skipped).

- [ ] **Step 7: Run the formatters and the full check**

Run: `nix develop --command pre-commit run --all-files`
Expected: PASS (nixfmt may reformat `beads-pkg.nix`; re-stage if it does)

- [ ] **Step 8: Commit**

```bash
git add templates/modules/beads-pkg.nix templates/modules/common.nix flake.nix
git commit -m "Install beads on instances and in the dev shell" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 2: Detect what beads mode a repository is in

**Files:**
- Create: `internal/beads/beads.go`
- Create: `internal/beads/detect.go`
- Test: `internal/beads/detect_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Mode int` with `ModeAbsent`, `ModeUnsynced`, `ModeGit`, `ModeExternal`; `func (Mode) String() string`
  - `type Detection struct { Mode Mode; ExternalURL string }`
  - `func Detect(ctx context.Context, repo string) (Detection, error)`
  - `func Available() bool`
  - `func run(ctx context.Context, repo string, args ...string) (string, error)` (unexported)

Note on the spec: it writes this as `Detect(repo) (Mode, error)` but also requires it to
report the external remote's URL so `"dolthub"` mode can reuse it. A two-field
`Detection` carries both without a second call.

- [ ] **Step 1: Confirm the real output format of `bd dolt remote list` before writing the parser**

Run, in a repository that has beads (the maintainer's own cloudlab checkout on the Mac):
```bash
bd dolt remote list
```
Record the exact output. The spec says it reports `origin` pointing at
`doltremoteapi.dolthub.com/jskswamy/cloudlab`. If the real format differs from the
fixtures below — extra header line, different column separator, a `name -> url` arrow —
**update the fixtures in Step 2 to match reality and keep the parser tolerant**. The
parser below already ignores a header and accepts any run of whitespace as the separator,
so most differences need only a fixture change.

- [ ] **Step 2: Write the failing test**

Create `internal/beads/detect_test.go`:

```go
package beads

import "testing"

func TestParseRemoteList(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []Remote
	}{
		{
			name: "empty output is no remotes",
			out:  "",
			want: nil,
		},
		{
			name: "a dolthub remote",
			out:  "origin https://doltremoteapi.dolthub.com/jskswamy/cloudlab\n",
			want: []Remote{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"}},
		},
		{
			name: "a session git remote",
			out:  "cloudlab-fix-auth git+ssh://subramk@100.1.2.3/home/subramk/sessions/fix-auth/cloudlab\n",
			want: []Remote{{Name: "cloudlab-fix-auth", URL: "git+ssh://subramk@100.1.2.3/home/subramk/sessions/fix-auth/cloudlab"}},
		},
		{
			name: "several remotes, ragged column alignment",
			out: "origin              https://doltremoteapi.dolthub.com/jskswamy/cloudlab\n" +
				"cloudlab-fix-auth   git+file:///home/subramk/sessions/fix-auth/cloudlab\n",
			want: []Remote{
				{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"},
				{Name: "cloudlab-fix-auth", URL: "git+file:///home/subramk/sessions/fix-auth/cloudlab"},
			},
		},
		{
			name: "blank lines and a header are ignored",
			out:  "NAME URL\n\norigin https://doltremoteapi.dolthub.com/jskswamy/cloudlab\n",
			want: []Remote{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRemoteList(tt.out)
			if len(got) != len(tt.want) {
				t.Fatalf("parseRemoteList() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("remote %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		remotes     []Remote
		wantMode    Mode
		wantExtURL  string
	}{
		{
			name:     "no remotes is unsynced",
			remotes:  nil,
			wantMode: ModeUnsynced,
		},
		{
			name:     "a git+ssh remote is git mode",
			remotes:  []Remote{{Name: "cloudlab-x", URL: "git+ssh://u@h/p"}},
			wantMode: ModeGit,
		},
		{
			name:     "a git+file remote is git mode",
			remotes:  []Remote{{Name: "cloudlab-x", URL: "git+file:///p"}},
			wantMode: ModeGit,
		},
		{
			name:       "an https remote is external",
			remotes:    []Remote{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"}},
			wantMode:   ModeExternal,
			wantExtURL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab",
		},
		{
			name:       "an aws remote is external",
			remotes:    []Remote{{Name: "origin", URL: "aws://[dynamo:s3]/beads"}},
			wantMode:   ModeExternal,
			wantExtURL: "aws://[dynamo:s3]/beads",
		},
		{
			// The repository this design was written in: DoltHub configured
			// AND a session remote wired. External wins, because the external
			// URL is the one thing the caller cannot reconstruct itself.
			name: "external wins when both are present",
			remotes: []Remote{
				{Name: "cloudlab-x", URL: "git+ssh://u@h/p"},
				{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"},
			},
			wantMode:   ModeExternal,
			wantExtURL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.remotes)
			if got.Mode != tt.wantMode {
				t.Errorf("classify().Mode = %v, want %v", got.Mode, tt.wantMode)
			}
			if got.ExternalURL != tt.wantExtURL {
				t.Errorf("classify().ExternalURL = %q, want %q", got.ExternalURL, tt.wantExtURL)
			}
		})
	}
}

func TestDetect_ReportsAbsentWhenTheRepoHasNoBeadsDir(t *testing.T) {
	// t.TempDir() has no .beads/, which is the ModeAbsent case: every beads
	// step downstream is skipped without ever invoking bd.
	got, err := Detect(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect() error = %v, want nil", err)
	}
	if got.Mode != ModeAbsent {
		t.Errorf("Detect().Mode = %v, want ModeAbsent", got.Mode)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `nix develop --command go test ./internal/beads/ -run 'TestParseRemoteList|TestClassify|TestDetect' -v`
Expected: FAIL — the package does not exist (`no Go files in .../internal/beads`)

- [ ] **Step 4: Write the package runner**

Create `internal/beads/beads.go`:

```go
// Package beads wires a repository's beads issue database into a cloudlab
// session, so the agent on the instance can read the issue it was given,
// close it, and file the follow-ups it finds.
//
// Everything here shells out to the `bd` binary; it is never linked as a
// library, matching how this codebase already treats sops, tailscale, nix
// and git.
//
// The transport is the session's own git repository. A dolt remote whose URL
// carries the git+ scheme stores the database as refs/dolt/data inside an
// ordinary git repository, and cloudlab already maintains exactly such a
// repository per session, reachable over the SSH channel the session's code
// already uses. No credential ever reaches the instance for this to work.
package beads

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// Available reports whether bd is on this machine's PATH. Callers use it to
// skip rather than to fail: a Mac without beads installed is a supported
// configuration, it just gets a session with no issue tracker.
func Available() bool {
	_, err := exec.LookPath("bd")
	return err == nil
}

// Present reports whether repo has a beads database at all. Checked before
// invoking bd, so the overwhelmingly common case -- a repository that has
// never used beads -- costs a stat rather than a process.
func Present(repo string) bool {
	info, err := os.Stat(filepath.Join(repo, ".beads"))
	return err == nil && info.IsDir()
}

// run executes bd inside repo and returns its combined output.
//
// CombinedOutput, not Output: bd reports the interesting part of a failure on
// stderr, and every caller here puts that text into the warning or error it
// surfaces.
func run(ctx context.Context, repo string, args ...string) (string, error) {
	// #nosec G204 -- argv-array exec.Command, no shell; repo is the user's
	// own repository path and args are built by this package.
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return string(out), err
}
```

- [ ] **Step 5: Write the detector**

Create `internal/beads/detect.go`:

```go
package beads

import (
	"context"
	"fmt"
	"strings"
)

// Mode is what a repository's beads database is currently synced against.
// Read from the repository rather than declared in cloudlab.pkl: .beads/
// already records it, and a second copy in config would only drift.
type Mode int

const (
	// ModeAbsent -- no beads database here. Every beads step is skipped.
	ModeAbsent Mode = iota
	// ModeUnsynced -- beads is present but has no dolt remote at all.
	ModeUnsynced
	// ModeGit -- synced over a git remote (git+ssh:// or git+file://).
	ModeGit
	// ModeExternal -- synced against DoltHub, Azure or Hosted Dolt.
	ModeExternal
)

func (m Mode) String() string {
	switch m {
	case ModeAbsent:
		return "absent"
	case ModeUnsynced:
		return "unsynced"
	case ModeGit:
		return "git"
	case ModeExternal:
		return "external"
	}
	return "unknown"
}

// Remote is one entry from `bd dolt remote list`.
type Remote struct {
	Name string
	URL  string
}

// Detection is what Detect found. ExternalURL is set only for ModeExternal,
// and exists so "dolthub" mode can reuse the URL the repository already
// records instead of restating it in cloudlab.pkl.
type Detection struct {
	Mode        Mode
	ExternalURL string
}

// Detect reports how repo's beads database syncs.
//
// It answers two questions and no others: whether beads exists here at all,
// and what the external remote's URL is. It never selects behaviour by
// itself -- cloudlab.pkl does that.
func Detect(ctx context.Context, repo string) (Detection, error) {
	if !Present(repo) || !Available() {
		return Detection{Mode: ModeAbsent}, nil
	}
	out, err := run(ctx, repo, remoteListArgs()...)
	if err != nil {
		return Detection{}, fmt.Errorf("bd dolt remote list in %s: %w\n%s", repo, err, out)
	}
	return classify(parseRemoteList(out)), nil
}

// remoteListArgs is the one bd invocation Detect makes.
func remoteListArgs() []string {
	return []string{"dolt", "remote", "list"}
}

// parseRemoteList reads `bd dolt remote list` output into name/URL pairs.
//
// Deliberately forgiving: any run of whitespace separates the two columns, a
// header line is skipped by the URL-shaped check below, and anything that is
// not two fields is ignored. The alternative -- a strict format -- would turn
// a cosmetic change in a tool cloudlab does not own into a failed session.
func parseRemoteList(out string) []Remote {
	var remotes []Remote
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// A URL, not a header: every dolt remote URL has a scheme.
		if !strings.Contains(fields[1], "://") {
			continue
		}
		remotes = append(remotes, Remote{Name: fields[0], URL: fields[1]})
	}
	return remotes
}

// classify turns a remote list into a Mode.
//
// External wins when a repository has both, which is the case this design was
// written in: the external URL is the one thing a caller cannot reconstruct
// for itself, whereas the session's git+ URL is built from the session name
// and the instance address it already holds.
func classify(remotes []Remote) Detection {
	if len(remotes) == 0 {
		return Detection{Mode: ModeUnsynced}
	}
	found := Detection{Mode: ModeUnsynced}
	for _, r := range remotes {
		if strings.HasPrefix(r.URL, "git+") {
			if found.Mode == ModeUnsynced {
				found.Mode = ModeGit
			}
			continue
		}
		return Detection{Mode: ModeExternal, ExternalURL: r.URL}
	}
	return found
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/beads/ -v`
Expected: PASS

- [ ] **Step 7: Vet and lint**

Run: `nix develop --command bash -c 'go vet ./... && golangci-lint run ./...'`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/beads/
git commit -m "Detect how a repository's beads database syncs" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 3: Name the session's dolt remote and build its commands

**Files:**
- Create: `internal/beads/remote.go`
- Create: `internal/beads/instance.go`
- Test: `internal/beads/remote_test.go`
- Test: `internal/beads/instance_test.go`

**Interfaces:**
- Consumes: `run`, `remoteListArgs` from Task 2.
- Produces:
  - `func RemoteName(session string) string`
  - `func RemoteURL(user, host, repoPath string) string`
  - `func FileURL(repoPath string) string`
  - unexported arg builders: `addRemoteArgs`, `removeRemoteArgs`, `pushArgs`, `pullArgs`
  - unexported instance command builders: `initCmd`, `remoteAddCmd`, `pushCmd`, `versionCmd`

- [ ] **Step 1: Write the failing test for the Mac-side builders**

Create `internal/beads/remote_test.go`:

```go
package beads

import (
	"slices"
	"testing"
)

func TestRemoteName_MirrorsTheSessionGitRemote(t *testing.T) {
	// Same name as lifecycle.sessionRemote, so `git remote` and
	// `bd dolt remote list` show the session under one name, not two.
	if got := RemoteName("fix-auth"); got != "cloudlab-fix-auth" {
		t.Errorf("RemoteName() = %q, want %q", got, "cloudlab-fix-auth")
	}
}

func TestRemoteURL_CarriesTheGitPlusSchemeExplicitly(t *testing.T) {
	// The git+ prefix must be written out, never left for bd to infer: with
	// an external sync.remote configured, `bd dolt remote add` appends a bare
	// path to the DoltHub base URL and produces a remote that fails at push.
	got := RemoteURL("subramk", "100.1.2.3", "/home/subramk/sessions/fix-auth/cloudlab")
	want := "git+ssh://subramk@100.1.2.3/home/subramk/sessions/fix-auth/cloudlab"
	if got != want {
		t.Errorf("RemoteURL() = %q, want %q", got, want)
	}
}

func TestFileURL_HasThreeSlashesBeforeAnAbsolutePath(t *testing.T) {
	// git+file://<empty host>/home/... -- the instance bootstraps from its
	// own copy of the repository, locally and offline.
	got := FileURL("/home/subramk/sessions/fix-auth/cloudlab")
	want := "git+file:///home/subramk/sessions/fix-auth/cloudlab"
	if got != want {
		t.Errorf("FileURL() = %q, want %q", got, want)
	}
}

func TestMacSideArgBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"add", addRemoteArgs("cloudlab-x", "git+ssh://u@h/p"),
			[]string{"dolt", "remote", "add", "cloudlab-x", "git+ssh://u@h/p"}},
		{"remove", removeRemoteArgs("cloudlab-x"),
			[]string{"dolt", "remote", "remove", "cloudlab-x"}},
		{"push", pushArgs("cloudlab-x"),
			[]string{"dolt", "push", "--remote", "cloudlab-x"}},
		{"pull", pullArgs("cloudlab-x"),
			[]string{"dolt", "pull", "--remote", "cloudlab-x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/beads/ -run TestRemote -v`
Expected: FAIL — `undefined: RemoteName`

- [ ] **Step 3: Implement the Mac-side builders**

Create `internal/beads/remote.go`:

```go
package beads

// RemoteName is the dolt remote pointing at a session's repository on the
// instance. Deliberately the same name lifecycle.sessionRemote gives the git
// remote: they address the same repository over the same channel, and one
// name for one thing is what makes `git remote -v` and `bd dolt remote list`
// legible side by side.
func RemoteName(session string) string {
	return "cloudlab-" + session
}

// RemoteURL is the dolt remote URL the Mac pushes issue data to: the
// session's own repository, over the SSH channel its code already uses.
//
// The git+ prefix is written out rather than left for bd to infer. `bd dolt
// remote add` normalises a bare filesystem path to git+file:// only when no
// external remote is configured; with a DoltHub sync.remote present it
// instead appends the path to the DoltHub base URL and produces a nonsense
// remote that fails at push. Establishing that cost a rehearsal, so it is
// recorded here rather than rediscovered.
func RemoteURL(user, host, repoPath string) string {
	return "git+ssh://" + user + "@" + host + repoPath
}

// FileURL is the same repository addressed from the instance itself. The
// instance bootstraps from this rather than from DoltHub even in "dolthub"
// mode: there is one bootstrap path in both modes -- local, offline, needing
// no credential -- for a database the session repository already holds.
func FileURL(repoPath string) string {
	return "git+file://" + repoPath
}

func addRemoteArgs(name, url string) []string {
	return []string{"dolt", "remote", "add", name, url}
}

func removeRemoteArgs(name string) []string {
	return []string{"dolt", "remote", "remove", name}
}

func pushArgs(name string) []string {
	return []string{"dolt", "push", "--remote", name}
}

func pullArgs(name string) []string {
	return []string{"dolt", "pull", "--remote", name}
}
```

- [ ] **Step 4: Write the failing test for the instance-side command builders**

Create `internal/beads/instance_test.go`:

```go
package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every instance command must run inside a login shell: bd comes from the
// instance user's home-manager profile (~/.nix-profile/bin), and a
// non-interactive SSH command runs a non-login shell that never sources the
// profile scripts putting that on PATH. Same reasoning as
// lifecycle.remoteGitCmd.
func TestInstanceCommands_RunInALoginShell(t *testing.T) {
	cmds := map[string]string{
		"init":      initCmd("/home/u/sessions/s/repo", "git+file:///home/u/sessions/s/repo"),
		"remoteAdd": remoteAddCmd("/home/u/sessions/s/repo", "dolthub", "https://doltremoteapi.dolthub.com/j/c"),
		"push":      pushCmd("/home/u/sessions/s/repo"),
		"version":   versionCmd("/home/u/sessions/s/repo"),
	}
	for name, cmd := range cmds {
		if !strings.HasPrefix(cmd, "bash -lc ") {
			t.Errorf("%s = %q, want a bash -lc wrapper", name, cmd)
		}
	}
}

func TestInitCmd_IsStealthAndCarriesTheRemote(t *testing.T) {
	got := initCmd("/home/u/sessions/s/repo", "git+file:///home/u/sessions/s/repo")
	// --stealth regardless of the Mac's mode: it keeps bd from writing its
	// own tracked files into the checkout, which a checkpoint's `git add -A`
	// would otherwise commit.
	for _, want := range []string{
		"bd init",
		"--stealth",
		"--remote",
		"git+file:///home/u/sessions/s/repo",
		"/home/u/sessions/s/repo",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("initCmd() = %q, want it to contain %q", got, want)
		}
	}
}

func TestPushCmd_PushesFromTheSessionRepo(t *testing.T) {
	got := pushCmd("/home/u/sessions/s/repo")
	if !strings.Contains(got, "bd dolt push") {
		t.Errorf("pushCmd() = %q, want it to contain %q", got, "bd dolt push")
	}
	if !strings.Contains(got, "/home/u/sessions/s/repo") {
		t.Errorf("pushCmd() = %q, want it to name the repo directory", got)
	}
}

// A path with a space is not hypothetical -- the repo name comes from the
// user's own directory name -- and an unquoted one would split into two
// arguments inside the login shell. Asserted by running the command through a
// real shell and checking where it landed, because string-matching a nest of
// quotes tests the assertion's own cleverness rather than the command.
func TestInstanceCommands_QuoteTheRepoPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my repo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	// `bd` need not exist for this: `cd '<dir>' && bd ...` reports the shell's
	// working directory in its failure, and a mis-quoted path fails at the cd
	// instead -- two different errors, which is exactly the distinction.
	got := instanceCmd(dir, "version")
	// #nosec G204 -- test-only, command built by this package.
	out, _ := exec.Command("bash", "-c", got+"; pwd").CombinedOutput()
	if !strings.Contains(string(out), dir) {
		t.Errorf("instanceCmd(%q) did not cd into the directory:\n%s", dir, out)
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `nix develop --command go test ./internal/beads/ -run TestInstance -v`
Expected: FAIL — `undefined: initCmd`

- [ ] **Step 6: Implement the instance-side builders**

Create `internal/beads/instance.go`:

```go
package beads

import (
	"strings"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// instanceCmd wraps a bd invocation for the instance.
//
// bash -lc is mandatory, not stylistic: bd comes from the instance user's
// home-manager profile (~/.nix-profile/bin/bd), and a non-interactive SSH
// command runs a non-login shell that never sources the profile scripts
// putting that on PATH. Every argument is shell-quoted because the login
// shell re-parses the whole string. Same shape as lifecycle.remoteGitCmd.
func instanceCmd(repo string, args ...string) string {
	quoted := make([]string, 0, len(args)+3)
	quoted = append(quoted, "cd", reconcile.ShellQuote(repo), "&&", "bd")
	for _, a := range args {
		quoted = append(quoted, reconcile.ShellQuote(a))
	}
	return "bash -lc " + reconcile.ShellQuote(strings.Join(quoted, " "))
}

// initCmd clones the session's issue database out of the git ref the Mac
// seeded, into the instance's checkout.
//
// --stealth regardless of what mode the Mac is in. bd's non-stealth init
// writes files bd expects to be tracked, and the instance's checkpoint runs
// `git add -A` on every pull -- so anything left in the working tree lands in
// a commit that merge then cherry-picks onto the user's branch under their
// signature. See lifecycle's excludeBeadsCmd for the other half of that
// guard.
//
// --remote adopts the project identity and issue prefix from the cloned
// database, so issues keep their cloudlab- names on the instance rather than
// being renamed after whatever the directory happens to be called.
func initCmd(repo, url string) string {
	return instanceCmd(repo, "init", "--stealth", "--remote", url)
}

// remoteAddCmd gives the instance a second destination to push to. Only used
// in "dolthub" mode, and only after the git+file:// bootstrap has succeeded.
func remoteAddCmd(repo, name, url string) string {
	return instanceCmd(repo, "dolt", "remote", "add", name, url)
}

// pushCmd publishes the agent's issue edits into the session repository's own
// refs/dolt/data, where the Mac's pull can reach them over SSH.
func pushCmd(repo string) string {
	return instanceCmd(repo, "dolt", "push")
}

// versionCmd reads the instance's bd version, compared against the Mac's
// because a shared Dolt database is the one place a version gap does real
// damage.
func versionCmd(repo string) string {
	return instanceCmd(repo, "version")
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/beads/ -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/beads/
git commit -m "Build the beads commands both machines run" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 4: Sync issues over the session's git remote

**Files:**
- Create: `internal/beads/sync.go`
- Test: `internal/beads/sync_test.go`
- Test: `internal/beads/roundtrip_test.go`

**Interfaces:**
- Consumes: everything from Tasks 2 and 3.
- Produces:
  - `func Wired(ctx context.Context, localRepo, session string) bool`
  - `func Seed(ctx context.Context, localRepo, session, url string) error`
  - `func Bootstrap(client *reconcile.Client, repo, fileURL, externalURL string) error`
  - `func Pull(ctx context.Context, localRepo, session string, client *reconcile.Client, repo string) error`
  - `func Unpulled(ctx context.Context, localRepo, session string, client *reconcile.Client, repo string) (bool, error)`

- [ ] **Step 0: Confirm bd's own CLI surface before writing the test**

Run: `nix develop --command bash -c 'bd create --help; bd list --help; bd dolt push --help'`

The test below uses `bd create "<title>"`, `bd list`, `bd init --stealth [--remote URL]`
and `bd dolt push`. Those were read off the spec and the tool's documented architecture,
not observed — confirm each subcommand's real spelling and adjust the test to match before
running it. Do not invent flags the help output does not show.

- [ ] **Step 1: Write the failing round-trip test**

This is the test the whole design rests on. `git+file://` drives the identical code path
as `git+ssh://`, so the complete round trip runs locally with no instance and no network.

Create `internal/beads/roundtrip_test.go`:

```go
package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireBd skips when bd is not installed. It is in the Linux dev shell (see
// flake.nix) so this runs in CI; on a Mac the derivation has no darwin entry
// and this test is skipped.
func requireBd(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("bd not on PATH")
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	// #nosec G204 -- test-only, all arguments built by this test.
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// initGitRepo makes a repository with one commit on main. A branch and a
// commit are both required before dolt data can be pushed into it: pushing to
// a git remote with no branches fails with "git remote has no branches ...
// initialize the repository with an initial branch/commit first".
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "init", "--quiet")
	mustRun(t, dir, "git", "checkout", "--quiet", "-b", "main")
	mustRun(t, dir, "git", "config", "user.email", "t@example.com")
	mustRun(t, dir, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "first")
}

// TestRoundTrip_IssuesTravelToTheSessionRepoAndBack is the design's central
// claim, exercised end to end: the Mac seeds its issue database into the
// session repository's refs/dolt/data, the instance bootstraps from that same
// repository over git+file://, an agent files an issue there, and the Mac
// pulls it home.
func TestRoundTrip_IssuesTravelToTheSessionRepoAndBack(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")

	initGitRepo(t, mac)
	initGitRepo(t, session)

	// The Mac's own beads database, with one issue in it.
	mustRun(t, mac, "bd", "init", "--stealth")
	mustRun(t, mac, "bd", "create", "Read me on the instance")

	// Seed: register the session remote and push the database into it.
	url := FileURL(session)
	if err := Seed(t.Context(), mac, "fix-auth", url); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	refs := mustRun(t, session, "git", "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/dolt/") {
		t.Fatalf("session repo has no dolt refs after Seed:\n%s", refs)
	}

	// Bootstrap the instance side from that same repository, then file an
	// issue as the agent would and push it back into the ref.
	mustRun(t, session, "bd", "init", "--stealth", "--remote", FileURL(session))
	mustRun(t, session, "bd", "create", "Filed by the agent")
	mustRun(t, session, "bd", "dolt", "push")

	// Pull it home.
	if _, err := run(t.Context(), mac, pullArgs(RemoteName("fix-auth"))...); err != nil {
		t.Fatalf("bd dolt pull: %v", err)
	}
	list := mustRun(t, mac, "bd", "list")
	if !strings.Contains(list, "Filed by the agent") {
		t.Fatalf("the agent's issue did not come home:\n%s", list)
	}
}

func TestWired_ReportsWhetherTheSessionRemoteExists(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")
	initGitRepo(t, mac)
	initGitRepo(t, session)
	mustRun(t, mac, "bd", "init", "--stealth")

	if Wired(t.Context(), mac, "fix-auth") {
		t.Error("Wired() = true before Seed, want false")
	}
	if err := Seed(t.Context(), mac, "fix-auth", FileURL(session)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	if !Wired(t.Context(), mac, "fix-auth") {
		t.Error("Wired() = false after Seed, want true")
	}
}

// The regression the spec names: a dolt remote added while an external
// sync.remote is configured must resolve to a git+ URL, not one prefixed with
// the external base URL.
func TestSeed_KeepsTheGitSchemeWhenAnExternalRemoteIsConfigured(t *testing.T) {
	requireBd(t)

	root := t.TempDir()
	mac := filepath.Join(root, "mac")
	session := filepath.Join(root, "session-repo")
	initGitRepo(t, mac)
	initGitRepo(t, session)
	mustRun(t, mac, "bd", "init", "--stealth")
	mustRun(t, mac, "bd", "dolt", "remote", "add", "origin",
		"https://doltremoteapi.dolthub.com/example/example")

	if err := Seed(t.Context(), mac, "fix-auth", FileURL(session)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	out, err := run(t.Context(), mac, remoteListArgs()...)
	if err != nil {
		t.Fatalf("bd dolt remote list: %v\n%s", err, out)
	}
	for _, r := range parseRemoteList(out) {
		if r.Name != RemoteName("fix-auth") {
			continue
		}
		if !strings.HasPrefix(r.URL, "git+file://") {
			t.Fatalf("session remote resolved to %q, want a git+file:// URL — "+
				"bd appended the path to the external base URL", r.URL)
		}
		return
	}
	t.Fatalf("no remote named %s after Seed:\n%s", RemoteName("fix-auth"), out)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/beads/ -run 'TestRoundTrip|TestWired|TestSeed' -v`
Expected: FAIL — `undefined: Seed`, `undefined: Wired`

If it reports `--- SKIP: bd not on PATH`, Task 1 Step 4 was not done or you are on
darwin. On Linux, fix Task 1 before continuing — this test is the point.

- [ ] **Step 3: Implement the sync operations**

Create `internal/beads/sync.go`:

```go
package beads

import (
	"context"
	"fmt"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// Wired reports whether beads was set up for this session on this machine.
//
// The presence of the session's dolt remote is the whole answer, and it is a
// better one than cloudlab.pkl could give: pull, merge, delete and down never
// resolve a config -- down runs from any directory -- and a session started
// while beads was off must stay off for the rest of its life regardless of
// what the file says now. Same principle the spec applies to mode detection,
// one level further down.
//
// False on any failure, including bd being absent. Callers use it to skip.
func Wired(ctx context.Context, localRepo, session string) bool {
	if !Present(localRepo) || !Available() {
		return false
	}
	out, err := run(ctx, localRepo, remoteListArgs()...)
	if err != nil {
		return false
	}
	for _, r := range parseRemoteList(out) {
		if r.Name == RemoteName(session) {
			return true
		}
	}
	return false
}

// Seed registers the session's dolt remote on this machine and publishes the
// issue database into it.
//
// Must run after the session branch has been pushed and checked out: pushing
// dolt data to a git remote with no branches fails outright with "git remote
// has no branches ... initialize the repository with an initial branch/commit
// first". seedSession's existing init -> push -> checkout order already
// satisfies that, so beads work simply goes last.
//
// Re-adding an existing remote is an error and a session may be started again
// after a failed attempt, so any stale one is dropped first -- the same shape
// trackSession uses for the git remote.
func Seed(ctx context.Context, localRepo, session, url string) error {
	remote := RemoteName(session)
	_, _ = run(ctx, localRepo, removeRemoteArgs(remote)...)
	if out, err := run(ctx, localRepo, addRemoteArgs(remote, url)...); err != nil {
		return fmt.Errorf("registering dolt remote %s: %w\n%s", remote, err, out)
	}
	if out, err := run(ctx, localRepo, pushArgs(remote)...); err != nil {
		return fmt.Errorf("seeding issues into session %s: %w\n%s", session, err, out)
	}
	return nil
}

// Bootstrap clones the seeded database into the instance's checkout, and in
// "dolthub" mode adds the external remote as a second destination afterwards.
//
// The bootstrap itself always runs from fileURL, never from the external
// remote: one bootstrap path in both modes -- local, offline, needing no
// credential -- for a database the session repository already holds.
// Bootstrapping from DoltHub instead would make session start depend on
// network reachability and on a credential.
//
// externalURL empty means session mode; nothing else about this changes.
func Bootstrap(client *reconcile.Client, repo, fileURL, externalURL string) error {
	if out, err := client.Run(initCmd(repo, fileURL)); err != nil {
		return fmt.Errorf("initialising issues on the instance: %w\n%s", err, out)
	}
	if externalURL == "" {
		return nil
	}
	if out, err := client.Run(remoteAddCmd(repo, "dolthub", externalURL)); err != nil {
		return fmt.Errorf("adding the external dolt remote on the instance: %w\n%s", err, out)
	}
	return nil
}

// Pull brings the agent's issue edits home: the instance publishes them into
// the session repository's own refs/dolt/data, and this machine fetches and
// merges from there over the SSH remote it already has.
//
// Conflicts surface from `bd dolt pull` and are reported, never
// auto-resolved.
func Pull(ctx context.Context, localRepo, session string, client *reconcile.Client, repo string) error {
	if out, err := client.Run(pushCmd(repo)); err != nil {
		return fmt.Errorf("publishing the agent's issues on the instance: %w\n%s", err, out)
	}
	if out, err := run(ctx, localRepo, pullArgs(RemoteName(session))...); err != nil {
		return fmt.Errorf("pulling issues from session %s: %w\n%s", session, err, out)
	}
	return nil
}

// Unpulled reports whether the instance still holds issue work this machine
// does not have.
//
// It answers by performing the sync rather than by comparing refs: this
// machine's database lives in .beads/embeddeddolt and has no local
// refs/dolt/data to diff against, so there is nothing to compare. A round
// trip that demonstrably completed is the evidence -- and it is exactly the
// guarantee the callers need, since anything they could not establish they
// must treat as unsafe.
//
// Returns (false, nil) only when both halves succeeded. Every failure is an
// error, and its callers refuse to destroy on one.
func Unpulled(ctx context.Context, localRepo, session string, client *reconcile.Client, repo string) (bool, error) {
	if err := Pull(ctx, localRepo, session, client, repo); err != nil {
		return true, err
	}
	return false, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/beads/ -v`
Expected: PASS (on Linux; the three `requireBd` tests SKIP on darwin)

If `TestSeed_KeepsTheGitSchemeWhenAnExternalRemoteIsConfigured` fails with a
DoltHub-prefixed URL, `bd dolt remote add` normalised the path despite the explicit
`git+file://` scheme. That would invalidate the spec's stated workaround — stop and
report it rather than working around it here.

- [ ] **Step 5: Vet and lint**

Run: `nix develop --command bash -c 'go vet ./... && golangci-lint run ./...'`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/beads/
git commit -m "Carry issues between the Mac and a session" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 5: Keep the Dolt database out of every checkpoint commit

This is the sharpest failure mode in the design and it is worth its own task and its own
regression test. Three facts compound: `checkpointCmd` runs `git add -A` on every pull;
`bd init` creates `.beads/embeddeddolt` inside the session checkout (3.8 MB in this
repository today); and `.git/info/exclude` does not travel over `git push`, so the Mac's
exclusions are absent from the instance's freshly initialised repository. Unexcluded, the
first `session pull` commits a multi-megabyte database, and `session merge` then
cherry-picks it onto the user's branch under their signature.

**Files:**
- Modify: `internal/lifecycle/gitremote.go`
- Test: `internal/lifecycle/beads_localgit_test.go`

**Interfaces:**
- Consumes: `remoteGitCmd`, `checkpointCmd` (existing, `internal/lifecycle/gitremote.go`).
  Test helpers come from the same package and are used as-is, not redefined:
  `runShell(t, command) (string, error)` and `gitOut(t, repo, args...) string` from
  `session_localgit_test.go`, `initRepo(t, repo)` and `mustGit(t, repo, args...)` from
  `session_test.go`. Note `mustGit` returns nothing — use `gitOut` when you need output.
- Produces: `func excludeBeadsCmd(repo string) string` and `const beadsDirPattern`
  (both unexported).

- [ ] **Step 1: Write the failing regression test**

It runs the real command strings through a real shell against a real repository, the way
`session_localgit_test.go` does — so the command itself is under test rather than a
paraphrase of it. No `bd` needed: the property is cloudlab's own, and a directory with a
file in it stands in for the database exactly.

Create `internal/lifecycle/beads_localgit_test.go`:

```go
package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExcludeBeadsCmd_KeepsTheDatabaseOutOfTheCheckpoint is the guard against
// the design's sharpest failure mode: checkpointCmd runs `git add -A`, bd
// writes a multi-megabyte .beads/embeddeddolt into the checkout, and
// .git/info/exclude does not travel over `git push` -- so the instance's
// repository has none of the Mac's exclusions unless cloudlab writes them.
func TestExcludeBeadsCmd_KeepsTheDatabaseOutOfTheCheckpoint(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	before := gitOut(t, repo, "rev-parse", "HEAD")

	if out, err := runShell(t, excludeBeadsCmd(repo)); err != nil {
		t.Fatalf("excludeBeadsCmd: %v\n%s", err, out)
	}

	// Stand in for what `bd init` leaves behind.
	db := filepath.Join(repo, ".beads", "embeddeddolt")
	if err := os.MkdirAll(db, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "data"), []byte("dolt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := runShell(t, checkpointCmd(repo, "cloudlab: checkpoint s")); err != nil {
		t.Fatalf("checkpointCmd: %v\n%s", err, out)
	}

	if after := gitOut(t, repo, "rev-parse", "HEAD"); after != before {
		files := gitOut(t, repo, "show", "--name-only", "--pretty=format:", "HEAD")
		t.Fatalf("the checkpoint committed the beads database:\n%s", files)
	}
}

// Idempotent: session start is retry-safe, so this runs again on every retry
// and must not accumulate entries.
func TestExcludeBeadsCmd_IsIdempotent(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	for range 3 {
		if out, err := runShell(t, excludeBeadsCmd(repo)); err != nil {
			t.Fatalf("excludeBeadsCmd: %v\n%s", err, out)
		}
	}

	content, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(content), beadsDirPattern); n != 1 {
		t.Errorf("exclude file has %d copies of %q, want 1:\n%s", n, beadsDirPattern, content)
	}
}

// A repository that never had .git/info/exclude, or never had the info
// directory at all, must still get the exclusion.
func TestExcludeBeadsCmd_CreatesTheExcludeFileWhenAbsent(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	if err := os.RemoveAll(filepath.Join(repo, ".git", "info")); err != nil {
		t.Fatal(err)
	}

	if out, err := runShell(t, excludeBeadsCmd(repo)); err != nil {
		t.Fatalf("excludeBeadsCmd: %v\n%s", err, out)
	}

	content, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("exclude file was not created: %v", err)
	}
	if !strings.Contains(string(content), beadsDirPattern) {
		t.Errorf("exclude file lacks %q:\n%s", beadsDirPattern, content)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestExcludeBeadsCmd -v`
Expected: FAIL — `undefined: excludeBeadsCmd`, `undefined: beadsDirPattern`

- [ ] **Step 3: Implement the command**

Append to `internal/lifecycle/gitremote.go`:

```go
// beadsDirPattern is the ignore entry that keeps a session's issue database
// out of the instance's checkpoint commits. A sibling of worktreeDirPattern,
// which does the same job on this machine.
const beadsDirPattern = "/.beads/"

// excludeBeadsCmd makes sure .beads/ is ignored in the instance's repository.
//
// Three facts compound into the reason this exists. checkpointCmd runs `git
// add -A` on every pull. `bd init` creates .beads/embeddeddolt inside the
// session checkout -- 3.8 MB in this repository today. And .git/info/exclude
// does not travel over `git push`, so the Mac's own exclusions are absent
// from the repository `git init` just made over here. Unexcluded, the first
// pull commits a multi-megabyte database and merge cherry-picks it onto the
// user's branch under their signature.
//
// Written by cloudlab before `bd init` runs, rather than relying on bd to
// write it: setup is fail-safe, so a `bd init` that fails partway is
// tolerated -- but it can still leave .beads/ behind, and by then the guard
// has to already be in place.
//
// .git/info/exclude rather than .gitignore, matching excludeWorktreeDir: it
// is cloudlab's own bookkeeping, not something to add to a file the user
// commits and reviews. grep -qxF makes it idempotent, which session start's
// retry-safety requires.
func excludeBeadsCmd(repo string) string {
	q := reconcile.ShellQuote
	// --absolute-git-dir, not --git-dir: the latter answers ".git", relative
	// to the -C directory, and this command never cd's -- so the exclusion
	// would land under the login shell's own home directory instead of the
	// session repository, silently doing nothing.
	exclude := "\"$(git -C " + q(repo) + " rev-parse --absolute-git-dir)\"/info/exclude"
	inner := "set -e" +
		"; e=" + exclude +
		"; mkdir -p \"$(dirname \"$e\")\"" +
		"; touch \"$e\"" +
		"; grep -qxF " + q(beadsDirPattern) + " \"$e\"" +
		" || printf '\\n# cloudlab session issue database\\n%s\\n' " + q(beadsDirPattern) + " >> \"$e\""
	return "bash -lc " + reconcile.ShellQuote(inner)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestExcludeBeadsCmd -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Run the whole lifecycle suite, to be sure nothing else moved**

Run: `nix develop --command go test ./internal/lifecycle/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/lifecycle/gitremote.go internal/lifecycle/beads_localgit_test.go
git commit -m "Exclude the issue database from checkpoint commits" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 6: Add the beads field to the config schema

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify: `internal/config/Config.pkl.go` (regenerated, not hand-edited)
- Test: `internal/config/config_test.go`
- Modify: `docs/config.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config.Beads string` (`"session"` | `"dolthub"` | `"off"`, default `"session"`).

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`, following the file's existing style for
fixture-based Resolve tests (read the neighbouring tests first and match their helper
usage — they write a temp `cloudlab.pkl` and call `Resolve`):

```go
func TestResolve_BeadsDefaultsToSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudlab.pkl")
	if err := os.WriteFile(path, []byte(`region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(t.Context(), path)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// The default has to be inert on a repository with no .beads/, so an
	// existing config needs no change at all -- see beads.Detect, which
	// returns ModeAbsent and skips every step.
	if cfg.Beads != "session" {
		t.Errorf("Beads = %q, want %q", cfg.Beads, "session")
	}
}

func TestResolve_BeadsAcceptsDolthubAndOff(t *testing.T) {
	for _, want := range []string{"dolthub", "off"} {
		t.Run(want, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "cloudlab.pkl")
			body := `region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
beads = "` + want + `"
`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Resolve(t.Context(), path)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if cfg.Beads != want {
				t.Errorf("Beads = %q, want %q", cfg.Beads, want)
			}
		})
	}
}

func TestResolve_BeadsRejectsAnUnknownMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudlab.pkl")
	if err := os.WriteFile(path, []byte(`region = "blr1"
size = "s-2vcpu-4gb"
template = "python"
beads = "dolthubb"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.Context(), path); err == nil {
		t.Fatal("Resolve() error = nil, want a type error naming the allowed modes")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/config/ -run TestResolve_Beads -v`
Expected: FAIL — `cfg.Beads undefined (type Config has no field or method Beads)`

- [ ] **Step 3: Add the field to the Pkl schema**

In `internal/config/Config.pkl`, after `tailscale`:

```pkl
/// How the instance's beads issue database syncs.
///
/// "session" (default): dolt data rides refs/dolt/data on the session's
/// own git remote over SSH; no credential reaches the instance.
/// "dolthub": additionally sync against the external remote already
/// configured in the repo's .beads. Requires dolthub_creds and
/// dolthub_creds_id in the personal secrets file.
/// "off": do not wire beads at all.
///
/// Says only whether to ship a credential -- never which mode the
/// repository is in. `bd dolt remote list` is authoritative for that, and
/// a second copy here would only drift.
beads: "session"|"dolthub"|"off" = "session"
```

- [ ] **Step 4: Regenerate the Go type**

Run: `make generate`
Expected: `internal/config/Config.pkl.go` gains a `Beads string` field with the doc
comment. Do not hand-edit that file — it is generated.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 6: Check the merge behaviour did not need a change**

`beads` is a scalar with a non-nil default, so `mergeConfig` treats it like `arch` and
`image` rather than like the pointer fields. Read `mergeConfig` in
`internal/config/config.go` and confirm the new field is handled the same way those two
are. If scalars are merged field-by-field there, add `beads` alongside them and extend
the neighbouring merge test; if it copies the struct, nothing is needed.

Run: `nix develop --command go test ./internal/config/`
Expected: PASS

- [ ] **Step 7: Document the field**

In `docs/config.md`, add a `beads` row/section beside `tailscale`, matching the file's
existing format:

```markdown
### `beads`

`"session"` (default) | `"dolthub"` | `"off"`

How the agent's issue database reaches the instance.

`"session"` carries it over the session's own git remote as `refs/dolt/data` — the same
repository, over the same SSH channel, that the session's code already uses. No
credential reaches the VM.

`"dolthub"` additionally ships your DoltHub credential so the instance can sync against
the external remote your `.beads/` already names. The credential is account-wide and
lives in tmpfs on the VM for its lifetime; the session remote is still wired, so issues
come home over SSH even when DoltHub is unreachable. Requires `dolthub_creds` and
`dolthub_creds_id` in `cloudlab secrets`.

`"off"` wires nothing.

The setting is inert in a repository with no `.beads/`.
```

- [ ] **Step 8: Commit**

```bash
git add internal/config/Config.pkl internal/config/Config.pkl.go internal/config/config_test.go docs/config.md
git commit -m "Add a beads mode to the config schema" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 7: Wire beads into session start

**Files:**
- Create: `internal/lifecycle/beads.go`
- Modify: `internal/provider/provider.go`
- Modify: `internal/lifecycle/session.go:60-92` (`seedSession`)
- Modify: `internal/lifecycle/session.go:38-50` (`StartSession` signature)
- Modify: `cmd/lookup_run.go:159-209` (`runSessionStart`)
- Test: `internal/provider/provider_test.go`
- Test: `internal/lifecycle/beads_test.go`

**Interfaces:**
- Consumes: `beads.Detect`, `beads.Seed`, `beads.Bootstrap`, `beads.RemoteName`,
  `beads.RemoteURL`, `beads.FileURL`, `excludeBeadsCmd`.
- Produces:
  - `func provider.ReportWarning(ctx context.Context, msg string)`
  - `func lifecycle.StartSession(ctx context.Context, ip, user, localRepo, repoName, session, beadsMode string) error` — **note the added trailing parameter**
  - `func lifecycle.seedBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, user, host, session, beadsMode string)` (unexported, never returns an error)

- [ ] **Step 1: Write the failing test for the warning channel**

Every beads step warns and continues, and there is no warning helper in this codebase
yet. Append to `internal/provider/provider_test.go`:

```go
func TestReportWarning_WritesToTheErrorWriter(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := WithOutput(context.Background(), &out, &errOut)

	ReportWarning(ctx, "beads: could not seed issues")

	if got := errOut.String(); !strings.Contains(got, "beads: could not seed issues") {
		t.Errorf("errOut = %q, want it to contain the warning", got)
	}
	if got := errOut.String(); !strings.HasPrefix(got, "warning: ") {
		t.Errorf("errOut = %q, want a \"warning: \" prefix", got)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing — a warning is not progress", out.String())
	}
}
```

Read `internal/provider/provider.go:83-90` first and match `WithOutput`'s real signature;
adjust the call above if it differs.

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/provider/ -run TestReportWarning -v`
Expected: FAIL — `undefined: ReportWarning`

- [ ] **Step 3: Implement the warning channel**

Append to `internal/provider/provider.go`:

```go
// ReportWarning tells the user something went wrong that was not fatal.
//
// Distinct from ReportProgress, which narrates what is happening on the happy
// path: a warning is what a fail-safe step emits when it gives up and carries
// on. Written to the error writer so a caller rendering progress into a
// viewport does not have to filter it back out of the progress stream.
func ReportWarning(ctx context.Context, msg string) {
	_, errOut := Output(ctx)
	_, _ = fmt.Fprintln(errOut, "warning: "+msg)
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `nix develop --command go test ./internal/provider/ -run TestReportWarning -v`
Expected: PASS

- [ ] **Step 5: Write the failing test for the seeding wrapper**

Create `internal/lifecycle/beads_test.go`:

```go
package lifecycle

import (
	"bytes"
	"context"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// Beads must never fail a session. seedBeads returns nothing at all, so there
// is no error for a caller to accidentally propagate -- the type enforces the
// policy rather than a comment asking callers to remember it.
func TestSeedBeads_IsSilentWhenTheModeIsOff(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// A nil client would panic if anything downstream ran; "off" must return
	// before touching it.
	seedBeads(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "u", "h", "s", "off")

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence — \"off\" is a choice, not a problem", errOut.String())
	}
}

func TestSeedBeads_IsSilentWhenTheRepoHasNoBeads(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// t.TempDir() has no .beads/, which is the overwhelmingly common case:
	// the default mode must be completely inert there, not merely harmless.
	seedBeads(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "u", "h", "s", "session")

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence for a repository with no beads", errOut.String())
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestSeedBeads -v`
Expected: FAIL — `undefined: seedBeads`

- [ ] **Step 7: Implement the seeding wrapper**

Create `internal/lifecycle/beads.go`:

```go
package lifecycle

import (
	"context"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// seedBeads gives the session's agent the repository's issue tracker: the
// dolt database rides refs/dolt/data on the session's own git remote, over
// the SSH channel its code already uses.
//
// Returns nothing. Every step here warns and continues, and a signature with
// no error is how that policy is enforced rather than merely documented -- a
// session start with a broken beads setup still produces a working session,
// which is the whole point of keeping this additive.
//
// Runs last in seedSession, after the branch has been pushed and checked out.
// Pushing dolt data into a git remote with no branches fails outright, so the
// existing init -> push -> checkout order is a precondition, not a
// coincidence.
func seedBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, user, host, session, beadsMode string) {
	if beadsMode == "off" {
		return
	}
	detected, err := beads.Detect(ctx, localRepo)
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not read this repository's dolt remotes, skipping: "+err.Error())
		return
	}
	if detected.Mode == beads.ModeAbsent {
		return
	}

	// Before bd init, not after. Setup is fail-safe, so a bd init that fails
	// partway is tolerated -- but it can still leave .beads/ behind, and an
	// unexcluded .beads/ is swallowed by the next checkpoint's `git add -A`
	// and cherry-picked onto the user's branch by merge.
	if out, err := client.Run(excludeBeadsCmd(repo)); err != nil {
		provider.ReportWarning(ctx, "beads: could not exclude .beads/ on the instance, skipping to avoid committing the database: "+err.Error()+"\n"+out)
		return
	}

	provider.ReportProgress(ctx, "seeding issues into "+session)
	if err := beads.Seed(ctx, localRepo, session, beads.RemoteURL(user, host, repo)); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
		return
	}

	// Only in "dolthub" mode, and only from a repository that actually has an
	// external remote to reuse -- cloudlab.pkl says whether to ship a
	// credential, never what the URL is.
	external := ""
	if beadsMode == "dolthub" && detected.Mode == beads.ModeExternal {
		external = detected.ExternalURL
	}
	if err := beads.Bootstrap(client, repo, beads.FileURL(repo), external); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
	}
}
```

- [ ] **Step 8: Run it to verify it passes**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestSeedBeads -v`
Expected: PASS

- [ ] **Step 9: Call it from seedSession**

In `internal/lifecycle/session.go`, thread `beadsMode` through and append the beads work
to `seedSession`, after the checkout:

```go
func StartSession(ctx context.Context, ip, user, localRepo, repoName, session, beadsMode string) error {
```

and pass it down:

```go
	if err := seedSession(ctx, ip, user, localRepo, repo, branch, url, host, beadsMode); err != nil {
		return err
	}
```

then in `seedSession`, after the `checkoutSessionCmd` call and before `return nil`:

```go
	if out, err := client.Run(checkoutSessionCmd(repo, branch)); err != nil {
		return fmt.Errorf("checking out %s on instance: %w\n%s", branch, err, out)
	}

	// Last, deliberately. Beads never fails a session, so this returns
	// nothing -- but it also has to run after the push and checkout above,
	// because pushing dolt data into a git remote with no branches fails.
	seedBeads(ctx, client, localRepo, repo, user, host, session, beadsMode)
	return nil
}
```

Update `seedSession`'s own signature to accept `host` and `beadsMode`.

- [ ] **Step 10: Resolve the mode in the command layer (D2)**

In `cmd/lookup_run.go`, add a helper beside `runSessionStart`:

```go
// beadsModeFor reads the beads setting out of the repository's cloudlab.pkl.
//
// Best-effort by design. `cloudlab session start` does not require pkl today,
// and beads must not be the reason it starts to: a config that will not
// resolve is a problem `up` and `provision` report properly, with a better
// message than this could give. The fallback is inert on a repository with no
// .beads/, which is the overwhelmingly common case.
func beadsModeFor(ctx context.Context, root string) string {
	cfg, err := config.Resolve(ctx, filepath.Join(root, "cloudlab.pkl"))
	if err != nil {
		return "session"
	}
	return cfg.Beads
}
```

and pass it at the call site:

```go
	if err := lifecycle.StartSession(ctx, record.IP, record.User, root, name, session, beadsModeFor(ctx, root)); err != nil {
		return err
	}
```

Add `"path/filepath"` and `"github.com/jskswamy/cloudlab/internal/config"` to the imports
if they are not already there.

- [ ] **Step 11: Fix every other caller and run the whole suite**

Run: `nix develop --command go build ./...`
Expected: compile errors naming each `StartSession` call site with the old arity —
including in `cmd/lookup_run_test.go` and `internal/lifecycle/session_test.go`. Update
each to pass `"session"` (the default) unless the test is specifically about a mode.

Run: `nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add internal/provider/ internal/lifecycle/ cmd/
git commit -m "Seed a session's issue tracker at start" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 8: Bring the agent's issues home on pull and merge

**Files:**
- Modify: `internal/lifecycle/beads.go`
- Modify: `internal/lifecycle/session.go` (`PullSession`, `MergeSession`)
- Test: `internal/lifecycle/beads_test.go`

**Interfaces:**
- Consumes: `beads.Wired`, `beads.Pull` (Task 4); `provider.ReportWarning` (Task 7).
- Produces: `func lifecycle.pullBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, session string) bool` (unexported, never returns an error; the bool reports whether the sync succeeded, and is `true` when beads was never wired).

Called from `PullSession` and `MergeSession` directly, **not** from the shared
`RescueSession`. Putting it in `RescueSession` looks tidier — all four verbs run through
it — but `delete` and `down` also call `RescueSession` and then call
`requireBeadsLanded` (Task 9), which syncs again: two round trips per teardown, and one
cause reported as both a warning and an error. One call site per verb keeps each verb's
beads behaviour visible where that verb is written.

`pullBeads` returns whether it synced, so `MergeSession` can say what is at stake.
`merge` removes the session repository, and that repository is where `refs/dolt/data`
lives — so a failed sync there is not a deferred problem, it is issue work about to be
destroyed. `merge` stays warn-and-continue rather than fail-closed (the spec puts the
guard on `delete` and `down`, and `merge` has no `--force` to escape with — a beads
problem must never strand a user's commits), but the warning has to name the loss.

- [ ] **Step 1: Write the failing test**

Append to `internal/lifecycle/beads_test.go`:

```go
func TestPullBeads_IsSilentWhenBeadsWasNeverWiredForTheSession(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// A session started while beads was off, or in a repository that never
	// had beads, must stay silent forever after -- pull runs on every
	// checkpoint and a warning per pull would train the user to ignore them.
	// A nil client would panic if anything downstream ran.
	if synced := pullBeads(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "s"); !synced {
		t.Error("pullBeads() = false, want true — nothing to sync is not a failed sync, " +
			"and merge decides whether to warn about losing issues from this")
	}

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence when beads was never wired", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want silence when beads was never wired", out.String())
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestPullBeads -v`
Expected: FAIL — `undefined: pullBeads`

- [ ] **Step 3: Implement it**

Append to `internal/lifecycle/beads.go`:

```go
// pullBeads brings the agent's issue edits home, reporting whether it
// managed to. Like seedBeads it never returns an error: a failed issue sync
// must never stop a pull that is otherwise making the agent's commits
// durable. The bool exists for merge, which is about to delete the
// repository the issues live in and has to say so.
//
// Silent when beads was never wired for this session. The session's dolt
// remote is the whole test -- pull, merge, delete and down never resolve a
// config, and a session started while beads was off must stay off for the
// rest of its life whatever cloudlab.pkl says now.
func pullBeads(ctx context.Context, client *reconcile.Client, localRepo, repo, session string) bool {
	if !beads.Wired(ctx, localRepo, session) {
		// Nothing to sync is not a failed sync: a caller weighing what it is
		// about to destroy must not be told issues are at risk when the
		// session never had any.
		return true
	}
	provider.ReportProgress(ctx, "pulling issues from "+session)
	if err := beads.Pull(ctx, localRepo, session, client, repo); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
		return false
	}
	return true
}
```

- [ ] **Step 4: Call it from PullSession**

`pullBeads` needs a live `*reconcile.Client`, and `PullSession` does not hold one — only
`RescueSession` opens it. Open a second, short-lived one after the rescue returns rather
than reshaping `RescueSession`'s signature for all four of its callers:

```go
	ref, _, err := RescueSession(ctx, ip, user, localRepo, repoName, session)
	if err != nil {
		return nil, err
	}

	// After the rescue, so a commit the agent made alongside an issue edit is
	// already safe if the issue sync then fails. A second connection rather
	// than a signature change on RescueSession: delete and down call that too,
	// and they gate on issues through requireBeadsLanded instead -- syncing
	// there as well would cost two round trips per teardown and report one
	// cause as both a warning and an error.
	if client, err := reconcile.Connect(ctx, ip, user); err == nil {
		pullBeads(ctx, client, localRepo, RemoteRepoPath(user, session, repoName), session)
		_ = client.Close()
	}
```

- [ ] **Step 5: Call it from MergeSession, naming what merge is about to destroy**

In `MergeSession`, right after its own `RescueSession` call:

```go
	ref, tip, err := RescueSession(ctx, ip, user, localRepo, repoName, session)
	if err != nil {
		return nil, err
	}

	// Before the replay, so the issue state matching the commits about to
	// land is already local. merge then removes the session repository --
	// which is where refs/dolt/data lives -- so a failed sync here is issue
	// work about to be destroyed, not a deferred problem. It still does not
	// refuse: merge has no --force, and a beads failure must never be the
	// thing that strands a user's commits on a box they then have to salvage
	// by hand. Saying plainly what is at stake is the honest middle.
	if client, err := reconcile.Connect(ctx, ip, user); err == nil {
		synced := pullBeads(ctx, client, localRepo, RemoteRepoPath(user, session, repoName), session)
		_ = client.Close()
		if !synced {
			provider.ReportWarning(ctx, fmt.Sprintf("beads: merge removes the session repository, and its issue database with it — any issue edits that did not sync are about to be lost; fix the problem and `cloudlab session pull %s` first to keep them", session))
		}
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -v`
Expected: PASS

- [ ] **Step 7: Run the whole suite**

Run: `nix develop --command bash -c 'go vet ./... && go test ./...'`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/lifecycle/
git commit -m "Bring a session's issues home on pull and merge" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 9: Refuse to destroy a session whose issues have not landed

The one place beads is fail-closed. `47118eb` already makes this guarantee for commits;
this extends it to issues, on the same reasoning: an unknown is treated as unsafe.

**Files:**
- Modify: `internal/lifecycle/beads.go`
- Modify: `internal/lifecycle/delete.go:24-49`
- Test: `internal/lifecycle/beads_test.go`

**Interfaces:**
- Consumes: `beads.Wired`, `beads.Unpulled` (Task 4).
- Produces: `func lifecycle.requireBeadsLanded(ctx context.Context, client *reconcile.Client, localRepo, repo, session string) error`.

Task 8 deliberately did **not** put the beads sync inside `RescueSession`, so `delete`
and `down` reach issues only through this guard — one sync per teardown, and it is the
one that gates.

- [ ] **Step 1: Write the failing test**

Append to `internal/lifecycle/beads_test.go`:

```go
func TestRequireBeadsLanded_PassesWhenBeadsWasNeverWired(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// The guard is fail-closed, but "never wired" is not an unknown -- it is
	// a known nothing. Treating it as unsafe would make every session in
	// every repository without beads undeletable.
	if err := requireBeadsLanded(ctx, nil, t.TempDir(), "/home/u/sessions/s/repo", "s"); err != nil {
		t.Errorf("requireBeadsLanded() error = %v, want nil when beads was never wired", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/lifecycle/ -run TestRequireBeadsLanded -v`
Expected: FAIL — `undefined: requireBeadsLanded`

- [ ] **Step 3: Implement the guard**

Append to `internal/lifecycle/beads.go`:

```go
// requireBeadsLanded refuses when the instance may still hold issue work this
// machine does not have.
//
// The one place beads is fail-closed, and it inverts everything else in this
// file deliberately. An error from Unpulled means cloudlab does not know
// whether the agent's issues are safe, and delete and down are the two verbs
// that make an unknown permanent -- so an unknown is treated as unsafe, the
// same stance 47118eb took for commits.
//
// A session beads was never wired for passes: that is a known nothing, not an
// unknown, and refusing on it would make every session in every repository
// without beads undeletable.
func requireBeadsLanded(ctx context.Context, client *reconcile.Client, localRepo, repo, session string) error {
	if !beads.Wired(ctx, localRepo, session) {
		return nil
	}
	unpulled, err := beads.Unpulled(ctx, localRepo, session, client, repo)
	if err != nil {
		return fmt.Errorf("cannot confirm session %s's issues have landed: %w\nrefusing to discard issue work cloudlab cannot see — fix the instance and retry, or pass --force to discard it anyway", session, err)
	}
	if unpulled {
		return fmt.Errorf("session %s still holds issue work that is not on this machine — `cloudlab session pull %s` takes it, or --force throws it away", session, session)
	}
	return nil
}
```

Add `"fmt"` to the file's imports.

- [ ] **Step 4: Call it from DeleteSession**

In `internal/lifecycle/delete.go`, inside the existing `if !force {` block, after the
commit checks and before the block closes:

```go
			if unmerged > 0 {
				return "", fmt.Errorf("session %s has %d commit(s) not on your branch — `cloudlab session merge %s` keeps them, or `cloudlab session delete %s --force` throws them away", s.Name, unmerged, s.Name, s.Name)
			}
		}

		// Issues too, on the same reasoning the commit checks above rest on:
		// delete is how work is thrown away, and the difference from merge
		// should never be discovered afterwards.
		if client, err := reconcile.Connect(ctx, ip, user); err == nil {
			err := requireBeadsLanded(ctx, client, s.LocalRepo, RemoteRepoPath(user, s.Name, repoName), s.Name)
			_ = client.Close()
			if err != nil {
				return "", err
			}
		}
	}
```

The `Connect` failure is deliberately not fatal here: `RescueSession` above already ran
and already failed loudly if the instance was unreachable, so a second connection error
at this point can only be a transient one on a box that just answered.

- [ ] **Step 5: Add the guard to `down`**

The spec says `delete` and `down` both "consult `Unpulled` and refuse". `down` reaches
sessions through `rescueBeforeDestroy`, so the guard goes there:

```go
	var firstErr error
	for _, s := range record.Sessions {
		provider.ReportProgress(ctx, "rescuing "+s.Name+" before destroy")
		if _, _, err := RescueSession(ctx, record.IP, record.User, s.LocalRepo, record.Name, s.Name); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("could not rescue session %s from %s: %w\n\nthe instance still exists and is still being billed.\n%d of %d sessions were checked; none were removed.\nfix and retry, or destroy anyway with: cloudlab down --force", s.Name, record.Name, err, len(record.Sessions), len(record.Sessions))
			continue
		}
		// Issues are the other half of what an agent produced, and down is
		// the verb that makes their loss permanent. RescueSession's own
		// beads step warns rather than failing, because a broken issue sync
		// must not stop a pull -- but it must stop a destroy.
		if client, err := reconcile.Connect(ctx, record.IP, record.User); err == nil {
			err := requireBeadsLanded(ctx, client, s.LocalRepo, RemoteRepoPath(record.User, s.Name, record.Name), s.Name)
			_ = client.Close()
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
```

Note the added `continue`: without it a session whose rescue failed would go on to have
its issues checked too, producing a second, more confusing error about the same box.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/lifecycle/ -v`
Expected: PASS. `delete_test.go` and `down_test.go` exercise the refusal paths against
the fake SSH server — read their existing cases and confirm none of them now take the new
branch unexpectedly. A test whose fake instance has no beads passes through `Wired`'s
`false` and is unaffected.

- [ ] **Step 7: Run the whole suite**

Run: `nix develop --command bash -c 'go vet ./... && golangci-lint run ./... && go test ./...'`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/lifecycle/
git commit -m "Refuse to discard a session's unlanded issues" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Task 10: Place the DoltHub credential for "dolthub" mode

**Files:**
- Create: `internal/reconcile/dolt.go`
- Modify: `internal/reconcile/reconcile.go:86-93` (end of `Reconcile`)
- Test: `internal/reconcile/dolt_test.go`
- Modify: `README.md:72-73`
- Modify: `docs/config.md`

**Interfaces:**
- Consumes: `Client.WriteSecretFile`, `Client.Run`, `ShellQuote` (existing);
  `secrets.Decrypt`, `secrets.Zero`; `config.Config.Beads` (Task 6);
  `provider.ReportWarning` (Task 7).
- Produces: `func placeDoltCredential(ctx context.Context, client *Client, beadsMode string)` (unexported, never returns an error).

- [ ] **Step 1: Write the failing test**

Create `internal/reconcile/dolt_test.go`, using the fake SSH server the existing
`ssh_test.go` already stands up (read `TestClient_WriteSecretFile_SendsContentViaStdinWithRestrictedMode`
at `internal/reconcile/ssh_test.go:267` and reuse its harness verbatim):

```go
package reconcile

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

func TestPlaceDoltCredential_DoesNothingOutsideDolthubMode(t *testing.T) {
	for _, mode := range []string{"session", "off", ""} {
		t.Run(mode, func(t *testing.T) {
			var out, errOut bytes.Buffer
			ctx := provider.WithOutput(context.Background(), &out, &errOut)
			// A nil client would panic if anything ran: the mode check must
			// come before any connection use at all.
			placeDoltCredential(ctx, nil, mode)
			if errOut.Len() != 0 {
				t.Errorf("errOut = %q, want silence in %q mode", errOut.String(), mode)
			}
		})
	}
}

func TestPlaceDoltCredential_WarnsAndContinuesWhenTheSecretIsMissing(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	// XDG_CONFIG_HOME at an empty temp dir means secrets.Path() names a file
	// that does not exist, which is exactly the "asked for dolthub, never ran
	// secrets init" case.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	placeDoltCredential(ctx, nil, "dolthub")

	got := errOut.String()
	if !strings.Contains(got, "dolthub_creds") {
		t.Errorf("errOut = %q, want it to name the missing key", got)
	}
	if !strings.Contains(got, "session") {
		t.Errorf("errOut = %q, want it to say sync falls back to session mode", got)
	}
}
```

The nil client in the second test is safe because the secrets lookup fails before the
client is ever used — assert that ordering by leaving it nil rather than by a comment.

- [ ] **Step 2: Run it to verify it fails**

Run: `nix develop --command go test ./internal/reconcile/ -run TestPlaceDoltCredential -v`
Expected: FAIL — `undefined: placeDoltCredential`

- [ ] **Step 3: Implement the placement**

Create `internal/reconcile/dolt.go`:

```go
package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/secrets"
)

// placeDoltCredential ships the user's DoltHub credential to the instance, so
// beads there can sync against the external remote directly.
//
// Per-instance, not per-session: a DoltHub credential identifies the user's
// account, not a session, so it is placed once by the path both `up` and
// `provision` run, and every session on the instance uses it. That is what
// lets N sessions share one instance with no per-session credential juggling,
// and it makes recovery after a reboot `cloudlab provision`, which is already
// idempotent.
//
// ~/.dolt is a symlink to $XDG_RUNTIME_DIR/cloudlab/dolt, which is tmpfs. bd
// and dolt find the credential where they always look, so nothing has to
// remember an environment variable, and the plaintext still only ever exists
// in tmpfs. DOLT_ROOT_PATH was rejected precisely because it would have to be
// threaded through ssh, tmux, herdr and the agent's own environment, and
// anything that missed it would silently fail to authenticate.
//
// Returns nothing. A missing credential warns and falls back to session mode
// rather than failing the reconcile -- deliberately unlike tailscale = true,
// which hard-fails on a missing key. A tailnet the user asked for and did not
// get is a broken instance; beads sharing has a working fallback that needs
// no credential at all.
func placeDoltCredential(ctx context.Context, client *Client, beadsMode string) {
	if beadsMode != "dolthub" {
		return
	}

	path, err := secrets.Path()
	if err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error()+"; issues will sync over the session remote only")
		return
	}
	provider.ReportProgress(ctx, "decrypting DoltHub credential (check for a YubiKey touch prompt)")
	cred, err := secrets.Decrypt(ctx, path, "dolthub_creds")
	if err != nil {
		provider.ReportWarning(ctx, "beads: no dolthub_creds in "+path+" ("+err.Error()+"); issues will sync over the session remote only")
		return
	}
	defer secrets.Zero(cred)

	// Stored beside the key rather than derived from this machine's
	// ~/.dolt/config_global.json, so the instance's configuration does not
	// depend on hidden local state.
	id, err := secrets.Decrypt(ctx, path, "dolthub_creds_id")
	if err != nil {
		provider.ReportWarning(ctx, "beads: no dolthub_creds_id in "+path+" ("+err.Error()+"); issues will sync over the session remote only")
		return
	}
	defer secrets.Zero(id)
	credsID := strings.TrimSpace(string(id))
	if credsID == "" {
		provider.ReportWarning(ctx, "beads: dolthub_creds_id is empty; issues will sync over the session remote only")
		return
	}

	// Resolved by a real remote round-trip rather than assumed, for the same
	// reason JoinTailscale resolves it that way: the result is a concrete
	// literal that can be shell-quoted like any other argument.
	runtimeDir, err := client.Run("bash -lc " + ShellQuote(`printf '%s' "$XDG_RUNTIME_DIR"`))
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not resolve the instance's runtime directory: "+err.Error()+"; issues will sync over the session remote only")
		return
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		provider.ReportWarning(ctx, "beads: the instance has no $XDG_RUNTIME_DIR; issues will sync over the session remote only")
		return
	}

	doltDir := runtimeDir + "/cloudlab/dolt"
	// An existing ~/.dolt that is not our symlink is left completely alone:
	// this neither clobbers a real directory nor writes an account-wide
	// credential to VM disk.
	prepare := "set -e" +
		"; mkdir -p " + ShellQuote(doltDir+"/creds") +
		"; chmod 700 " + ShellQuote(doltDir) +
		"; if [ -e \"$HOME/.dolt\" ] && [ ! -L \"$HOME/.dolt\" ]; then echo NOTASYMLINK; exit 0; fi" +
		"; ln -sfn " + ShellQuote(doltDir) + " \"$HOME/.dolt\""
	out, err := client.Run("bash -lc " + ShellQuote(prepare))
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not prepare ~/.dolt on the instance: "+err.Error()+"\n"+out)
		return
	}
	if strings.Contains(out, "NOTASYMLINK") {
		provider.ReportWarning(ctx, "beads: ~/.dolt on the instance is a real directory, not cloudlab's symlink — leaving it alone and not placing a credential; issues will sync over the session remote only")
		return
	}

	if err := client.WriteSecretFile(doltDir+"/creds/"+credsID+".jwk", cred); err != nil {
		provider.ReportWarning(ctx, "beads: writing the DoltHub credential: "+err.Error())
		return
	}
	globalConfig := fmt.Sprintf("{\"user.creds\":%q}\n", credsID)
	if err := client.WriteSecretFile(doltDir+"/config_global.json", []byte(globalConfig)); err != nil {
		provider.ReportWarning(ctx, "beads: writing the DoltHub config: "+err.Error())
	}
}
```

- [ ] **Step 4: Call it from Reconcile**

In `internal/reconcile/reconcile.go`, at the end of `Reconcile`, after the successful
home-manager switch:

```go
	output, err := client.RunStreaming(cmd, out, errOut)
	if err != nil {
		return fmt.Errorf("home-manager switch failed: %w\n%s", err, tail(output, 40))
	}

	// After the switch, not before: placing a credential on a box whose
	// environment failed to build helps nobody, and this is the path both
	// `up` and `provision` run -- so recovery after a reboot clears tmpfs is
	// `cloudlab provision`, which is already idempotent.
	placeDoltCredential(ctx, client, cfg.Beads)
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `nix develop --command go test ./internal/reconcile/ -v`
Expected: PASS

- [ ] **Step 6: Correct the README's invariant**

`README.md:72-73` currently reads, without qualification:

> The instance never pushes anywhere and never talks to a shared remote — no
> GitHub credentials, host keys or signing keys ever reach the VM.

`beads = "dolthub"` makes that conditional, and left unedited the documentation is simply
wrong. Rewrite it as:

```markdown
The instance never pushes anywhere and never talks to a shared remote — no
GitHub credentials, host keys or signing keys ever reach the VM. `sync`/
```

...becomes:

```markdown
The instance never pushes anywhere and never talks to a shared remote — no
GitHub credentials, host keys or signing keys ever reach the VM.

The one carve-out is opt-in: `beads = "dolthub"` in `cloudlab.pkl` places your
DoltHub credential on the instance, in tmpfs, so the agent's issue database can
sync against DoltHub directly. That credential is account-wide — it grants write
access to every Dolt repository on your account for the life of the instance.
The default, `beads = "session"`, needs no credential at all: issues ride the
session's own git remote over the SSH channel your code already uses. `sync`/
```

Keep the sentence that follows `sync`/ intact — read the surrounding paragraph and splice
rather than replacing it wholesale.

- [ ] **Step 7: Document the secrets keys**

In `docs/config.md`, in the `beads` section added in Task 6, append the two keys:

````markdown
`"dolthub"` mode reads two keys from `cloudlab secrets`:

```yaml
dolthub_creds: |            # contents of ~/.dolt/creds/<id>.jwk
  {"kty":"OKP","crv":"Ed25519",…}
dolthub_creds_id: us8isfuv…  # the jwk's filename stem, written to user.creds
```

The id is stored beside the key rather than derived from your local
`~/.dolt/config_global.json`, so the instance's configuration does not depend on hidden
local state. If either is missing, cloudlab warns and falls back to session mode.

A reboot clears tmpfs and leaves `~/.dolt` dangling; external sync then fails and session
mode is unaffected. `cloudlab provision` restores it.

`bd dolt push` creates a real `refs/heads/__dolt_remote_info__` branch in the session
repository alongside `refs/dolt/data`. It is harmless but visible in `git branch`.
````

- [ ] **Step 8: Run everything, including the checks CI runs**

Run:
```bash
nix develop --command bash -c 'go build ./... && go vet ./... && golangci-lint run ./... && go test ./...'
nix develop --command pre-commit run --all-files
nix flake check --print-build-logs
```
Expected: all PASS. `trufflehog` runs in pre-commit — the example `dolthub_creds` value in
`docs/config.md` is a truncated placeholder, not a key, but if it trips the scanner
shorten it further rather than adding an ignore rule.

- [ ] **Step 9: Commit**

```bash
git add internal/reconcile/ README.md docs/config.md
git commit -m "Place the DoltHub credential in dolthub mode" -m "$(cat <<'MSG'
<body>
MSG
)"
```

Replace `<body>` with a real commit body before running this: 2–5 sentences,
wrapped at 72 columns, saying *why* this change exists. The reasoning is already
above in this task — the problem it solves, the constraint that forced the shape
it took, what was rejected. `AGENTS.md` requires the body; a subject-only commit
is a defect, and this plan's Global Constraints say so too. Do not add an AI
co-author trailer.

---

## Finishing

- [ ] **Mark the spec implemented**

In `docs/superpowers/specs/2026-09-07-beads-in-sessions-design.md`, change
`Accepted, not implemented.` to `Accepted, implemented.` and note the three refinements
this plan made beyond it (D1, D2, D3) so the spec and the code do not silently disagree —
`AGENTS.md`: "A spec is the implementation contract; if the code and the spec disagree,
say so rather than silently diverging."

- [ ] **Verify against a real instance**

The Go suite covers everything except the `git+ssh://` leg, which no in-process harness
can reach. Once merged, run a real session end to end:

```bash
cloudlab session start beads-smoke
cloudlab ssh   # then, in the session checkout: bd list
               # expect the repository's real issues, with cloudlab- prefixes
               # then: bd create "Filed from the instance"
cloudlab session pull beads-smoke
bd list        # expect the instance's new issue
git log -1 --stat cloudlab-beads-smoke/cloudlab/beads-smoke
               # expect NO .beads/ paths in the checkpoint
cloudlab session delete beads-smoke
```

- [ ] **Then use superpowers:finishing-a-development-branch**

---

## Self-review

**Spec coverage.** `templates/modules/beads-pkg.nix` → Task 1. `internal/beads`
(`Detect`, `RemoteName`, `RemoteURL`, `Seed`, `Pull`, `Unpulled`) → Tasks 2–4.
`internal/config` → Task 6. `internal/secrets` (two keys, no code change — `Decrypt` is
already generic) → Task 10. `internal/reconcile` → Task 10. `excludeBeadsDir` → Task 5.
`session start` data flow, including the three ordering constraints → Tasks 5 and 7.
`session pull` → Task 8. `merge`/`delete`/`down` → Tasks 8 and 9. Error handling, the
checkpoint hazard, both named regression tests, mode detection as a table → Tasks 2, 4, 5.
README carve-out and the `__dolt_remote_info__` note → Task 10.

**Known gaps, deliberate.** The version-mismatch warning the spec mentions (`bd version`
compared across both machines) has a command builder in Task 3 (`versionCmd`) but no
caller: it is a warning about a condition the pinned derivation in Task 1 is designed to
prevent, and wiring it would add a remote round-trip to every session start for a check
that should never fire. `Detect` returns a `Detection` struct rather than the spec's
`(Mode, error)`, because it must also report the external URL. `Unpulled` proves the round
trip rather than diffing refs — see D3.
