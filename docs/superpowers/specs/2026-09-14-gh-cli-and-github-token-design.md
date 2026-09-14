# `gh` CLI and an optional GitHub token for the provisioned instance

Status: proposed
Date: 2026-09-14
Builds on: `docs/superpowers/specs/2026-09-13-ssh-key-discovery-design.md` (the
secrets/reconcile machinery this reuses), `templates/modules/common.nix`.

## The problem

A coding agent running on a cloudlab instance frequently needs to look things
up on GitHub — issues, PRs, releases, file contents on a public repo. `gh` is
the natural tool for that, and it isn't installed today.

`gh` already works against public repos with no authentication at all, so
this isn't strictly required — but unauthenticated requests are capped at
GitHub's anonymous rate limit (60 requests/hour, vs. 5,000/hour
authenticated), which an agent making several lookups per session can hit.
A token raises that ceiling. It also has to stay optional: not everyone
wants to hand a PAT to an instance a coding agent drives, and cloudlab
should not force that choice on anyone who just wants `gh` for public
repos.

Two independent decisions follow from that: install `gh` unconditionally
(cheap, no downside), and ship a token only when the user has chosen to
provide one (never required, never default-on).

## Installing `gh`

One line in `templates/modules/common.nix`'s `config.home.packages`, next
to `pkgs.git`, `pkgs.tmux`, `beads`, `pkgs.devbox`: `pkgs.gh`.

Unconditional, not gated by `cloudlab.pkl`'s `packages` list (free-form,
user-chosen) or `agents` (curated per-harness list) — `git`, `tmux`, `beads`
and `devbox` are already installed this way precisely because they're
useful to every instance regardless of per-project configuration, and `gh`
belongs in that set for the same reason. No new home-manager module, no new
`cloudlab.pkl` field.

## The `github_token` secret

A new key, `github_token`, in the existing sops-encrypted personal secrets
file (`~/.config/cloudlab/secrets.yaml`) — the same file that already holds
`digitalocean_token`, `tailscale_authkey`, `dolthub_creds` and
`dolthub_creds_id`. Edited the same way, via `cloudlab secrets edit`. No new
file: one sops file per user already covers every other per-user
credential, and a second file for one more key would be pure overhead.

**Purely optional, gated on presence alone** — no `cloudlab.pkl` field.
`up`/`provision` check whether `github_token` decrypts successfully; if it
does, the token is shipped and `gh` is configured; if it doesn't (file
missing, or file exists but has no `github_token` key), `gh` is left
unauthenticated and nothing more happens. This mirrors `resolveToken`'s and
`JoinTailscale`'s existing treatment of a missing secret as an ordinary,
non-fatal case — not a config toggle like `cloudlab.tailscale`, because this
isn't a feature that needs enabling, only a credential that's either there
or isn't.

## Shipping it to the instance

Reuses `internal/reconcile`'s existing shape almost exactly:
`placeGitHubToken(ctx, client)`, called from `reconcile.Reconcile`
alongside today's `placeDoltCredential(ctx, client, ...)` call
(`internal/reconcile/reconcile.go:129`). Because `Reconcile` is the one
path both `up` and `cloudlab provision` run through, this gets the same
idempotent-recovery story the design doc for SSH keys already established
for `tailscale_authkey` and `dolthub_creds`: the plaintext only ever lands
in `$XDG_RUNTIME_DIR` (tmpfs), a reboot wipes it, and recovery is "run
`cloudlab provision` again" — already idempotent, nothing new to build for
that case.

Sequence, modeled on `placeDoltCredentialFor`
(`internal/reconcile/dolt.go:125`):

1. `secrets.Decrypt(ctx, path, "github_token")`. Failure of any kind (file
   absent, key absent) → `provider.ReportProgress` with an informational
   line, return. Deliberately `ReportProgress`, not `ReportWarning`: an
   absent optional secret is not a degraded state worth a warning, unlike
   dolt's missing credential (which does fall back to a real warning,
   because that one has a config-declared feature — `beads = "dolthub"` —
   it can't fully deliver on).
2. Resolve the instance's `$XDG_RUNTIME_DIR` via a real remote round-trip
   (`printf '%s' "$XDG_RUNTIME_DIR"` over the login shell) — same as both
   `JoinTailscale` and `placeDoltCredentialFor` already do, and for the
   same reason: the result has to be a concrete literal safe to
   shell-quote into the commands that follow.
3. Prepare `~/.config/gh` as a symlink to
   `$XDG_RUNTIME_DIR/cloudlab/gh`, leaving anything else already at
   `~/.config/gh` (a real directory, a regular file, a symlink elsewhere)
   untouched — the same "not cloudlab's to touch" rule
   `doltPrepareScript` already enforces for `~/.dolt`.
4. `client.WriteSecretFile` the token, as `hosts.yml`, into that directory.
5. `secrets.Zero` the local copy, regardless of outcome.

### Shared symlink-prepare helper

`doltPrepareScript` (`internal/reconcile/dolt.go:63`) already builds
exactly the shell script step 3 needs, just hardcoded to `~/.dolt`. Rather
than write a second, near-identical ~15-line script builder for
`~/.config/gh`, generalize it into one helper both call sites use:

```go
// symlinkPrepareScript builds a shell script that points linkName (a
// dotfile under $HOME) at targetDir, leaving anything already there
// that isn't cloudlab's own symlink untouched. See doltPrepareScript's
// original comment for why -L is checked before -e, and why a foreign
// symlink is reported the same way as a real file or directory.
func symlinkPrepareScript(linkName, targetDir string) string
```

`doltPrepareScript(doltDir)` becomes `symlinkPrepareScript(".dolt",
doltDir)`; the new call site is `symlinkPrepareScript(".config/gh",
ghDir)`. Both callers keep their own `NOTASYMLINK` handling and their own
`ReportWarning`/`ReportProgress` wording, since what a foreign `~/.dolt` vs.
a foreign `~/.config/gh` means to the user differs.

## `hosts.yml` contents

```yaml
github.com:
    oauth_token: <token>
    git_protocol: https
```

`GH_TOKEN`/environment-variable auth was considered and rejected — it's
exactly the shape of fix `internal/reconcile/dolt.go`'s own comment already
rejected for `DOLT_ROOT_PATH`: an env var would have to be threaded through
SSH, tmux, herdr and the agent's own environment, and anything that missed
it fails silently. Writing to the file `gh` already reads by default is the
same fix dolt already applies to the identical problem, reused rather than
reinvented.

Omitting the `user:` field: current `gh` resolves it from the API when
absent. To confirm during implementation against the actual `gh` version
nixpkgs pins — if `gh auth status` or another subcommand turns out to want
it, the fix is resolving it with one extra `gh api user` call before
shipping the token, not a design change.

## Failure and skip behavior

| Condition | Outcome |
|---|---|
| No `secrets.yaml`, or no `github_token` key in it | Skip. One `ReportProgress` line noting `gh` will run unauthenticated (60 req/hour, fine for public repos). Not a warning, not a failure. |
| `~/.config/gh` exists and isn't cloudlab's own symlink | Skip. `ReportWarning`, same wording style as dolt's `NOTASYMLINK` case — a real `gh auth login` on the instance is not cloudlab's to overwrite. |
| Token decrypts and `~/.config/gh` is free | Written. `gh` authenticated from the next login shell onward. |

None of these fail `up` or `provision` outright — same as dolt's credential,
and unlike the SSH-key guard from the previous spec, because an
unauthenticated `gh` is a fully working, if rate-limited, fallback rather
than a broken instance.

## Components touched

| Unit | Change |
|---|---|
| `templates/modules/common.nix` | Add `pkgs.gh` to `config.home.packages`. |
| `internal/reconcile/dolt.go` | Extract `symlinkPrepareScript` from `doltPrepareScript`; update the one call site. |
| `internal/reconcile/github.go` (new) | `placeGitHubToken`, mirroring `dolt.go`'s shape. |
| `internal/reconcile/reconcile.go` | One added call, alongside the existing `placeDoltCredential` call at line 129. |
| `docs/config.md` | New row/section for `github_token`, styled on the existing `dolthub_creds`/`tailscale_authkey` sections. |

## Testing

- `symlinkPrepareScript`: extend the existing table test that covers
  `doltPrepareScript` today (already exercises the not-a-symlink,
  dangling-symlink, and already-correct-symlink cases against a scratch
  `$HOME`) to run parametrized over both call sites, or duplicate the table
  with `.config/gh` substituted — whichever keeps the test itself
  readable.
- `placeGitHubToken`: three cases, matching `placeDoltCredentialFor`'s
  existing shape — secret present and `~/.config/gh` free (writes
  `hosts.yml` with the right content); secret absent (no remote call at
  all); `~/.config/gh` already foreign (warns, writes nothing).
- No new Nix-side testing: the `common.nix` change is additive to an
  existing, already-validated `home.packages` list, covered by CI's
  existing `nix flake check` / home-manager build.

## Rejected alternatives

**`GH_TOKEN` exported via shell profile.** The exact failure mode
`DOLT_ROOT_PATH` was already rejected for: has to reach every entry point
(non-interactive SSH commands, tmux panes, herdr sessions, the agent's own
environment), and silently does nothing for whichever one it misses.

**A `cloudlab.pkl` boolean flag gating the token** (`githubToken = true`,
mirroring `cloudlab.tailscale`). Rejected per explicit direction: this is
meant to be a low-ceremony optional extra with no new config surface —
whether the secret exists is the only switch.

**Gating `gh`'s installation on `cloudlab.pkl`** (either a flag or the
free-form `packages` list). Rejected: the point was a base package every
instance gets, the same as `git`/`tmux`/`beads`/`devbox`, not a per-project
opt-in.

**A separate secrets file for GitHub credentials.** Rejected: one sops file
already holds every other per-user credential; a second file for one key
is pure overhead with no benefit `cloudlab secrets edit` doesn't already
give the existing file.
