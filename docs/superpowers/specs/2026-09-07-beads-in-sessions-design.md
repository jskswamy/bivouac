# Beads In Sessions

## Status

Accepted, not implemented.

## Context

An agent working in a session has the repository but not the issue tracker.
`.beads/` is excluded from git (`.git/info/exclude`), so it never rides the
seed commit, and the instance has no `bd` binary and no Dolt database. The
agent cannot read the issue it was given, cannot record what it did, and
cannot file the follow-ups it discovers. Today the only way to brief it is to
dump `bd show` into a file and rsync it in by hand.

Beads already solves cross-machine sharing twice over, and the two mechanisms
have very different costs here.

The first is an external Dolt remote — DoltHub, Azure, Hosted Dolt. It works
from anywhere with network access, and this repository is already configured
for it (`bd dolt remote list` reports `origin` pointing at
`doltremoteapi.dolthub.com/jskswamy/cloudlab`). The cost is a credential: a
DoltHub keypair at `~/.dolt/creds/<id>.jwk`, which grants write access to
every Dolt repository on the account, not just this one. Putting it on a
cloud VM contradicts the invariant the README states plainly — *"The instance
never pushes anywhere and never talks to a shared remote — no GitHub
credentials, host keys or signing keys ever reach the VM."*

The second is Dolt-in-git. Beads describes its own architecture as: *"issues
live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote;
`.beads/issues.jsonl` is a passive export."* A dolt remote whose URL carries
the `git+` scheme — `git+ssh://`, `git+file://` — stores the database as a
side ref in an ordinary git repository. That matters here because cloudlab
already maintains exactly such a repository per session, reachable over SSH,
with no credential beyond the key `up` already registered.

The second mechanism costs nothing and is therefore the default. The first
stays available, because new issues created on an instance need somewhere
durable to live when the user wants sharing that outlives the session.

## Decisions

**Two modes, detected rather than declared.** `bd dolt remote list` is
authoritative and the URL scheme discriminates: `git+ssh://` or `git+file://`
means git mode, an `https://`/`az://`/`aws://` URL means an external remote,
empty means beads is present but unsynced. cloudlab reads this from the
repository rather than asking the user to restate it.

**Git mode is the default and needs no configuration.** Every session gets a
dolt remote named `cloudlab-<session>` pointing at
`git+ssh://<user>@<host>/home/<user>/sessions/<session>/<repo>` — the same
repository, over the same SSH channel, that the session's code already uses.
No credential leaves the Mac. The README's invariant holds unchanged.

**External mode is additive and opt-in.** `beads = "dolthub"` in
`cloudlab.pkl` ships the DoltHub credential so the instance can sync directly.
The session `git+ssh` remote is still wired in this mode, so issues come home
over SSH even when DoltHub is unreachable. The modes compose; they do not
replace each other.

**Credentials are per-instance, sessions are per-session.** A DoltHub
credential identifies the user's account, not a session, so it is placed once
by `reconcile` — the path both `up` and `provision` run — and every session on
the instance uses it. Session commands never touch credentials. This is what
lets N sessions share one instance without per-session credential juggling,
and it makes recovery after a reboot `cloudlab provision`, which is already
idempotent.

**The credential lives at the standard path, backed by tmpfs.** `~/.dolt` is
a symlink to `$XDG_RUNTIME_DIR/cloudlab/dolt`, which holds `creds/<id>.jwk`
and `config_global.json`. `bd` and `dolt` find it where they always look, so
nothing has to remember an environment variable, and the plaintext still only
ever exists in tmpfs. `DOLT_ROOT_PATH` was rejected precisely because it would
have to be threaded through ssh, tmux, herdr and the agent's own environment,
and anything that missed it would silently fail to authenticate.

**Beads is installed like moshi-hook, not from nixpkgs.**
`templates/modules/beads-pkg.nix` ports the user's existing
`overlays/60-beads-latest.nix`, trimmed to the two Linux systems the template
flake builds. nixpkgs ships 1.0.3 against 1.1.2 locally; pinning the same
upstream release on both machines removes schema skew from a shared database
rather than hoping a minor gap is tolerable.

**Setup is fail-safe; destruction is fail-closed.** No beads, no `bd`, or any
failing `bd` command warns and continues — beads never fails a session. The
one exception is the guard on `session delete` and `down`: if cloudlab cannot
establish that the instance's issues have landed, it refuses to destroy,
matching the guarantee `47118eb` already makes for commits.

### Rejected alternatives

**sops-nix at activation (ADR-0009).** Decrypting at home-manager activation
would place the credential before any session exists, which is the right
shape on paper. Rejected because activation is the wrong lever: it cannot be
re-run selectively, it fails the whole reconcile when a secret is missing, and
the requirement here is explicitly that a beads problem never breaks an
instance. ADR-0009 remains the right design for long-running services that
need secrets at boot; it is not this.

**Persisting the credential to `~/.dolt` on disk.** Simplest to implement and
survives reboot, but writes an account-wide credential to VM disk, which
ADR-0006 ruled out and nothing here justifies revisiting.

**A read-only instance (`bd --readonly`).** Removes conflicts entirely, and
beads ships the flag for exactly this worker-sandbox case. Rejected because
an agent that cannot claim, close, or file follow-ups leaves the user doing
its bookkeeping by hand, which is most of the value.

**Declaring the mode in `cloudlab.pkl`.** A `beads = "git"|"remote"` enum
would duplicate what `.beads/` already records, and the two would drift.
The field that does exist says only whether to ship a credential.

## Architecture

```
Mac                                          instance
────────────────────────────────────────────────────────────────────────
.beads/embeddeddolt                          sessions/<s>/<repo>/.beads/
     │                                              │
     │  bd dolt push --remote cloudlab-<s>          │
     ├──────────── git+ssh ───────────────▶ refs/dolt/data
     │                                       (in the session repo)
     │  bd dolt pull --remote cloudlab-<s>          │
     ◀──────────── git+ssh ────────────────────────┘

                        ~/.dolt ──▶ $XDG_RUNTIME_DIR/cloudlab/dolt
                                    (tmpfs, "dolthub" mode only)
                                             │
                                             ▼
                                        DoltHub
```

The session repository is both the agent's checkout and the beads transport.
Its `refs/dolt/data` is written by the Mac at seed, read by the instance at
bootstrap, written by the instance as the agent works, and read back by the
Mac on pull. One ref, one channel, no third party.

## Components

### `templates/modules/beads-pkg.nix`

Beads ships prebuilt release tarballs, so this is a fetch and an install
rather than a Go build. The version and hashes below are the two Linux
entries of the maintainer's existing `overlays/60-beads-latest.nix`,
reproduced here so this spec is self-contained — the darwin entries in that
overlay are irrelevant, since the template flake builds only `x86_64-linux`
and `aarch64-linux`.

```nix
{ stdenv, fetchzip, lib }:
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
  src = sources.${stdenv.hostPlatform.system}
        or (throw "Unsupported system: ${stdenv.hostPlatform.system}");
in
stdenv.mkDerivation {
  pname = "beads";
  inherit version src;
  dontUnpack = true;
  installPhase = "install -Dm755 $src/bd $out/bin/bd";
  meta.mainProgram = "bd";
}
```

Wired into `common.nix` as `beads = pkgs.callPackage ./beads-pkg.nix { }`,
following the `moshiHook` precedent, and added to `home.packages`
unconditionally — it is a small binary, and gating a package on config would
buy nothing.

Bumping the version means changing it in both places, here and in the
maintainer's overlay, because a shared Dolt database is the one place a
version gap does real damage. That cost is recorded under Costs.

`dolt` itself is not installed: `bd` runs `dolt_mode: embedded`, an in-process
engine.

### `internal/beads`

A new package that shells out to `bd`, matching how this codebase already
treats `sops`, `tailscale` and `nix` — never linked as a library.

- `Detect(repo) (Mode, error)` — parses `bd dolt remote list`, returning
  `ModeGit`, `ModeExternal`, `ModeUnsynced`, or `ModeAbsent`. It answers two
  questions and no others: whether beads exists here at all (`ModeAbsent`
  skips every step below), and what the external remote's URL is, so
  `"dolthub"` mode can reuse it instead of restating it in `cloudlab.pkl`.
  It never selects behaviour by itself — `cloudlab.pkl` does that.
- `RemoteName(session)` — `cloudlab-<session>`, mirroring `sessionRemote`.
- `RemoteURL(user, host, repoPath)` — the `git+ssh://` URL.
- `Seed`, `Pull`, `Unpulled` — the operations the session lifecycle calls.

`Unpulled` reports whether the instance holds issue commits absent from the
Mac, and is the only function here whose error is fatal to its caller.

### `internal/config`

One field on the schema:

```pkl
/// How the instance's beads database syncs.
///
/// "session" (default): dolt data rides refs/dolt/data on the session's
/// own git remote over SSH; no credential reaches the instance.
/// "dolthub": additionally sync against the external remote configured
/// in the repo's .beads. Requires dolthub_creds in the personal
/// secrets file.
/// "off": do not wire beads at all.
beads: "session"|"dolthub"|"off" = "session"
```

The default is inert on a repository with no `.beads/`, so existing configs
need no change.

### `internal/secrets`

Two new keys, read only when `beads = "dolthub"`:

```yaml
dolthub_creds: |            # contents of ~/.dolt/creds/<id>.jwk
  {"kty":"OKP","crv":"Ed25519",…}
dolthub_creds_id: us8isfuv…  # the jwk's filename stem, written to user.creds
```

The id is stored beside the key rather than derived from the Mac's
`~/.dolt/config_global.json`, so the instance's configuration does not depend
on hidden local state.

### `internal/reconcile`

Places the credential when the resolved config asks for it: creates
`$XDG_RUNTIME_DIR/cloudlab/dolt` (0700), writes `creds/<id>.jwk` (0600) via
`Client.WriteSecretFile` and `config_global.json` beside it, then symlinks
`~/.dolt` at it. Decrypted bytes are zeroed immediately, as `JoinTailscale`
already does.

If `~/.dolt` exists and is not that symlink, reconcile warns and does nothing
— it neither clobbers a real directory nor writes a credential to disk.

### `internal/lifecycle/session.go`

`excludeBeadsDir`, a sibling of the existing `excludeWorktreeDir`, writing
`.beads/` into the instance repository's `.git/info/exclude`. Seed, pull and
the delete guard gain their beads counterparts.

## Data flow

### `session start`

Existing steps are unchanged; beads work is appended.

```
ensure repo, set identity, push HEAD:cloudlab/<s>, checkout     (existing)

exclude .beads/ in the instance repo's .git/info/exclude
mac:  bd dolt remote add cloudlab-<s> git+ssh://<u>@<h>/home/<u>/sessions/<s>/<repo>
      bd dolt push --remote cloudlab-<s>
inst: bd init --stealth --remote git+file:///home/<u>/sessions/<s>/<repo>
```

Three constraints fix this ordering, all established by rehearsal:

The `git+` scheme must be written explicitly. `bd dolt remote add` normalises
a bare filesystem path to `git+file://` only when no external remote is
configured; with a DoltHub `sync.remote` present it instead appends the path
to the DoltHub base URL and produces a nonsense remote that fails at push.

The dolt push must follow the branch checkout. Pushing to a git remote with no
branches fails with *"git remote has no branches … initialize the repository
with an initial branch/commit first"*. The seed's existing init → push →
checkout order already satisfies this, so beads work simply goes last.

The exclusion must precede `bd init`. Since setup is fail-safe, a `bd init`
that fails partway is tolerated — but it can leave `.beads/` behind, and an
unexcluded `.beads/` is swallowed by the next checkpoint (see Error handling).

`bd init --stealth --remote` clones the database from the ref and adopts the
project identity and issue prefix from it, so issues keep their `cloudlab-`
names on the instance rather than being renamed after the directory.

In `"dolthub"` mode one line is appended, and only one:

```
inst: bd dolt remote add dolthub <url from Detect>
```

The bootstrap still runs from `git+file://`. There is one bootstrap path in
both modes — local, offline, and already proven — and the external remote is
added afterwards as a second destination the agent can push to. Bootstrapping
from DoltHub instead would make session start depend on network reachability
and on a credential, for a database the session repository already holds.

### `session pull`

```
inst: bd dolt push                          (agent's issue edits -> refs/dolt/data)
mac:  bd dolt pull --remote cloudlab-<s>    (fetch + merge)
```

Runs after the existing checkpoint and fetch. Conflicts surface from
`bd dolt pull` and are reported, never auto-resolved.

### `session merge`, `delete`, `down`

`merge` pulls beads first, so the issue state matching the commits it is about
to land is already local. `delete` and `down` consult `Unpulled` and refuse
when issues have not landed, extending the existing rescue guarantee.

## Error handling

Every beads step warns and continues. A session start with a broken beads
setup still produces a working session; that is the whole point of keeping
this additive.

The delete and down guard inverts this. `Unpulled` returning an error means
cloudlab does not know whether the agent's issues are safe, and an unknown is
treated as unsafe — the same stance `47118eb` took for commits.

`beads = "dolthub"` with `dolthub_creds` missing warns and falls back to
session mode rather than failing. This differs deliberately from
`tailscale = true`, which hard-fails on a missing key: a tailnet the user
asked for and did not get is a broken instance, whereas beads sharing has a
working fallback that needs no credential.

A reboot clears tmpfs and leaves `~/.dolt` dangling. External sync fails;
session mode is unaffected, because it needs no credential. `cloudlab
provision` restores it.

`bd version` is compared across both machines and a mismatch warns, since a
shared Dolt database is the one place a version gap does real damage.

### The checkpoint hazard

Three facts compound into the sharpest failure mode in this design.
`checkpointCmd` runs `git add -A` on every pull. `bd init` creates
`.beads/embeddeddolt` inside the session checkout — 3.8 MB in this repository
today. And `.git/info/exclude` does not travel over `git push`, so the Mac's
exclusions are absent from the instance's freshly initialised repository.

Unexcluded, the first `session pull` commits a multi-megabyte Dolt database
into a checkpoint commit, and `session merge` then cherry-picks it onto the
user's branch under their signature.

This is why the instance is always initialised `--stealth` regardless of the
Mac's mode, and why cloudlab writes the exclusion itself before invoking `bd`
rather than relying on `bd init` having succeeded.

## Testing

Following `session_localgit_test.go`, which exists precisely because the push
in `seedSession` cannot be exercised against a real instance in-process.

`git+file://` drives the identical code path as `git+ssh://`, so the whole
round trip is testable locally: seed a repository, push beads data into a
second one, bootstrap from it, create an issue there, push, pull back, and
assert the issue arrives. This was rehearsed by hand before this spec was
written and works end to end.

Command builders are unit-tested in the style of `gitremote_test.go`. Two
regression tests earn their place specifically:

- A dolt remote added while an external `sync.remote` is configured resolves
  to a `git+` URL, not a DoltHub-prefixed one.
- After seeding beads and running pull, the checkpoint commit is empty —
  the guard against the hazard above.

Mode detection is table-driven over `bd dolt remote list` output.

## Costs

A DoltHub credential is account-wide. In `"dolthub"` mode it grants write to
every Dolt repository on the account for the life of the instance, in tmpfs on
a cloud VM. That is the price of the mode, and it is why the mode is opt-in
and the default needs no credential at all.

The README's *"never talks to a shared remote"* invariant becomes conditional
and needs an explicit carve-out naming `beads = "dolthub"`. Left unedited, the
documentation is simply wrong.

`bd dolt push` creates a real `refs/heads/__dolt_remote_info__` branch in the
session repository alongside `refs/dolt/data`. It is harmless but visible in
`git branch`, and worth documenting so it does not read as corruption.

Pinning beads in `templates/` means a version bump is now a cloudlab change as
well as a nixos-config one. Accepted in exchange for removing schema skew from
a database both machines write.

## Out of scope

Moving `DIGITALOCEAN_TOKEN` into the secrets file (`cloudlab-10c`). It uses
the same mechanism and is a natural follow-on, but it is an `up`-time concern
with no bearing on sessions.

Implementing ADR-0009. The sops-nix design remains the right answer for
secrets that long-running services need at boot; nothing here depends on it.

Reconciling `JoinTailscale`'s placement. It runs in `Up` but not `provision`,
so it does not survive a reboot the way this design's credential placement
does. The same class of gap, but a separate change.
