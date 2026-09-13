# SSH key discovery for the wizard's `sshKeys` question

Status: implemented
Date: 2026-09-13
Tracks: `cloudlab-pwg.4` (this design), `cloudlab-gvc` (decision), `cloudlab-d05` (implementation)
Builds on: `docs/superpowers/specs/2026-09-09-cloudlab-init-and-presets-design.md`

## The problem

`sshKeys` takes a DigitalOcean key ID or fingerprint that the user must
have registered by hand and must then recall from memory. Nothing in
cloudlab reads a local public key or asks DigitalOcean what the account
holds, and `cloudlab init` inherited that unchanged: a free-text input
that takes whatever is typed.

### What actually goes wrong

Three failure modes, and they are not equally bad. Getting this straight
is what decides how much machinery is worth building.

| Mode | Outcome | Cost |
|---|---|---|
| Fingerprint not on the account | `422 … are invalid key identifiers for Droplet creation` | Cheap — no droplet, nothing billed |
| Key is on the account, private half is not on this machine | Droplet boots, is billed, refuses your login | **Expensive and silent** |
| `sshKeys` left unset — the schema permits it | Droplet boots, is billed, refuses your login | **Expensive and silent** |

The first mode is loud and free. Its only real cost is confusion:
DigitalOcean has been observed reporting this condition as a *hostname*
validation error instead of naming the key, so the message can send you
looking at the wrong field entirely.

The second and third are the ones that matter. They cost a running
instance and are discovered at a login prompt, several minutes later,
with `down` and start again as the only cure.

This inverts the obvious reading. The danger is not mistyping a
fingerprint — DigitalOcean catches that for us. The danger is
confidently naming a key you cannot actually use, or naming none at all.

**So the design's first job is not validation. It is to offer keys the
user demonstrably holds.** Everything else is refinement.

## The observation that shapes everything

**A correct `sshKeys` value can be produced with no credentials and no
network.**

DigitalOcean's `Key.Fingerprint` is the RFC 4716 MD5 presentation —
colon-separated hex, no prefix. `x/crypto/ssh.FingerprintLegacyMD5`
returns exactly that string, and `x/crypto` is already a direct
dependency. Given a public key, cloudlab can compute the fingerprint
DigitalOcean matches on, locally and offline, in about five lines.

The API is therefore not needed to *answer* the question. It is needed
only for two follow-ups: is this key on the account, and shall I put it
there?

## Where the keys come from

Two sources, merged and deduplicated by fingerprint, **agent first**.

**The ssh-agent** (`SSH_AUTH_SOCK`) is the better source and the
non-obvious one. A key in the agent is a key that can authenticate right
now — that is what an agent is for. A `.pub` file on disk is weaker
evidence: its private half may be long gone, and selecting it produces
exactly the expensive-silent failure above.

It is also the only source that sees smartcard and YubiKey-resident
keys, which have no private half on disk at all. `internal/reconcile`
already enumerates agent keys and its comment makes this same point, so
the precedent and the code pattern both exist.

**`~/.ssh/*.pub`** covers the case of a key that is not loaded into an
agent right now, which is ordinary and should not be punished.

Requiring a matching private file was rejected: it would filter out dead
`.pub` files, but at the cost of excluding smartcard users entirely —
the most security-conscious users, locked out of the feature by a check
meant to protect them.

Globbing every `*.pub` looks inconsistent with `reconcile`'s deliberately
narrow `defaultIdentityFiles`, so: that list is narrow because every key
offered to an sshd spends one of six `MaxAuthTries`. Listing choices in a
form spends no such budget. The cost of an extra row is a line on screen;
the cost of a missing one is the user typing a fingerprint by hand.

Public keys are public. Nothing here opens a private key or needs a
secret.

## The three tiers

They degrade independently. Tier 1 alone produces a correct value.

| Tier | Needs | Adds |
|---|---|---|
| 1 | nothing | agent keys and `~/.ssh/*.pub`, by comment and fingerprint |
| 2 | token with SSH-key read | `registered` / `not on your account` labels; account keys with no local half |
| 3 | token with SSH-key create | offers to upload a key that is not on the account |

A free-text entry stays available at every tier. It is today's behaviour,
it is the only route for a key on neither side, and removing it would
make the wizard less capable than the file it writes.

### Tier 1, no token

```
Which SSH keys?
Keys on this machine. cloudlab cannot check these against your
DigitalOcean account without a token.

  [x] id_ed25519   you@laptop     in agent
  [ ] yubikey      (smartcard)    in agent
  [ ] id_rsa       old-desktop    on disk
  [ ] Type a fingerprint or key ID…

If a key is not registered with DigitalOcean, up fails with "invalid
key identifiers" — nothing is created or billed.
```

The warning is proportionate to the risk it describes: that failure is
free, so it earns a line of text, not a blocking check.

### Tier 2, token with read

```
Which SSH keys?

  [x] id_ed25519    you@laptop     in agent, registered
  [ ] yubikey       (smartcard)    in agent, registered
  [ ] id_rsa        old-desktop    on disk, not on your account
  [ ] work-laptop                  on your account, not on this machine
  [ ] Type a fingerprint or key ID…
```

Account keys with no local counterpart are **shown, labelled, and never
preselected**. The second-machine and setting-up-for-a-teammate cases are
real, so hiding them would force those users to the free-text entry. But
picking one is how the expensive-silent failure happens, so it has to be
a deliberate act rather than a default.

Preselected by default: keys that are both held locally and registered —
the ones that will certainly work.

### Tier 3, uploading

Selecting an unregistered key opens a separate, explicit step. Never
silent: uploading a key to someone's cloud account is a side effect
outside this tool's remit to assume.

```
id_rsa is not on your DigitalOcean account.
Upload it?   [ Upload ]  [ Skip ]

Name it:  old-desktop          ← the key's comment, else user@hostname
```

Three outcomes have to be handled:

- **Success.** `Keys.Create` with `{Name, PublicKey}`; the key is now
  registered and stays selected.
- **Already there.** A create can fail because the key is already on the
  account — a race, or a key present under another name. Do not match on
  the message text: re-list and check whether the fingerprint is now
  present, and if it is, treat the create as having succeeded. The goal
  is a registered key, not a create call.
- **No permission.** A 403 means the token cannot add keys. Say so, name
  where to fix it, deselect that key, and continue. `init` must not fail
  because an optional tier was unavailable.

## Components

| Unit | Responsibility |
|---|---|
| `internal/sshkeys` | enumerate agent keys and `~/.ssh/*.pub`, dedupe, fingerprint. No network, no provider. |
| `internal/provider`'s `KeyRegistry` | optional capability: list and create account keys |
| `internal/provider/digitalocean` | implements `KeyRegistry` over `godo.KeysService` |
| `internal/wizard` | tier selection and choice assembly, as pure functions |
| `cmd/init.go` | the multi-select and the upload prompt |

### `KeyRegistry` is a separate interface

```go
// KeyRegistry is implemented by providers that can enumerate and
// register SSH keys. Optional: callers type-assert for it.
type KeyRegistry interface {
	ListKeys(ctx context.Context) ([]SSHKey, error)
	CreateKey(ctx context.Context, name, publicKey string) (SSHKey, error)
}

type SSHKey struct {
	ID          string
	Name        string
	Fingerprint string
	PublicKey   string
}
```

Not a method on `Provider`. That interface is four lifecycle methods and
its own doc comment says provider-specific concepts stay out of it rather
than being forced into a cross-provider abstraction. Key management is
exactly such a concept, the wizard is its only caller, and a type
assertion at one call site is cheaper than a method every future provider
must implement to return "unsupported".

## Two rules the implementation must not lose

**The token is never reached for implicitly.** Tiers 2 and 3 run only
when `DIGITALOCEAN_TOKEN` is in the environment, or when the user
explicitly asks to check their account. They must never trigger
`resolveToken`'s sops fallback on their own: that decrypts through age,
commonly an `age-plugin-yubikey` identity whose touch policy waits for
physical presence with no output of its own — see `resolveToken`'s own
comment. A form silently blocked on a key nobody was asked to touch is
the same failure as one blocked on absent stdin, which the 2026-09-09
spec already calls indistinguishable from a hang.

The first run of `cloudlab init` on a fresh machine completes with no
credentials at all.

**A missing scope degrades, it does not fail.** A 403 from `ListKeys`
drops the flow to tier 1; a 403 from `CreateKey` drops that one key. A
narrowly scoped token is the *recommended* configuration — see
`cloudlab-24k` — so it must not break `init`.

## Fingerprints are written, not IDs

The DO provider's `sshKeys()` already accepts either: a numeric string
becomes an ID, anything else a fingerprint.

A fingerprint identifies the key material; a numeric ID identifies a row
in one account. The fingerprint survives deleting and re-adding the key,
means the same thing in a second account, and can be checked by hand
against `ssh-keygen -lE md5 -f ~/.ssh/id_ed25519.pub`. An ID reads as a
magic number in a committed-adjacent file.

## Token scopes

This answers what `cloudlab-24k` left open. A fine-grained PAT needs
droplet create/read/delete, plus SSH-key read for tier 2, plus SSH-key
create only if tier 3 is wanted. The console is authoritative on the
exact scope names.

## Testing

- Fingerprinting is a pure function over a public key's bytes: table
  test against known fingerprints, including a key with a comment, one
  without, and a file that is not a public key.
- Discovery against a temp `$HOME/.ssh` holding `.pub` files, private
  keys and unrelated files, plus a fake agent — `internal/reconcile`'s
  `startFakeAgent` is the existing pattern.
- Dedupe: the same key in both the agent and on disk appears once, and
  is reported as being in the agent.
- Tier and choice assembly is a pure function of (local keys, account
  keys or nil, create-capable or not): table test, no provider, no form.
- The upload outcomes against a fake `KeyRegistry`: success; create
  fails but a re-list shows the fingerprint, so it counts as success;
  403 deselects the key and the flow continues.
- `init` completes with no credentials present.
- The forms are covered by the units beneath them, not driven.

## Rejected alternatives

**Require a token for the question.** It is the first question of the
first run on a new machine. Gating it on credentials — worse, on
credentials that may wait for a YubiKey touch — makes the wizard useless
in the situation it exists for.

**Read the account's keys and skip local discovery.** Needs a token to
answer at all, and cannot tell which keys the user actually holds. That
is the expensive-silent failure, reintroduced as the primary flow.

**Require a private half on disk.** Would exclude smartcard and
agent-only keys — the users least likely to have a private file and most
likely to care.

**Upload without confirming.** Fewest keystrokes, but writing to
someone's cloud account is not a side effect a question-asking flow gets
to assume. It also makes the 403 path a surprise rather than an answer.

**Match the "already exists" error by its message text.** Brittle across
API wording changes. Re-listing and checking for the fingerprint asks the
question that actually matters.

**Compute SHA256 fingerprints for storage.** What `ssh-keygen -l` prints
today and what users recognise, but not what DigitalOcean matches on.
Showing SHA256 alongside in the form is a presentational choice; the
stored value has to be the one the API accepts.

**Generate a key when none is found.** Creating key material has
consequences well outside cloudlab. Say none were found and point at
`ssh-keygen`.

**Verify the stored key on every `up`.** Guards against a key deleted
from DigitalOcean after the file was written — but puts an API call on
the hot path of the most-scripted command, for a case that announces
itself at the next login. Revisit if it proves common.

## As implemented (2026-09-13)

Built as designed. Three notes for anyone reading the code against this
document.

**The free-text entry is a sentinel option.** huh's multi-select cannot
mix a text field into its list, so "Type a fingerprint or key ID…" is an
option whose value is a sentinel; selecting it opens an input afterwards
and its answer is appended. With nothing discovered at all there is no
list to show, so the question degrades to that input directly.

**Tier 1's default selection is the agent's keys**, and disk-only keys
are left unselected. The spec said "keys that are both held locally and
registered", which tier 1 cannot know. Selecting every local key was the
obvious alternative and is wrong: DigitalOcean refuses a create naming
any unregistered key, so one stale `.pub` would fail every later `up`
until the file was edited again. An agent key is the best available
evidence of a key in active use.

**An unregistered key that is not uploaded is dropped from the
selection**, rather than written and left to fail. The reasoning is the
same: one unregistered entry fails the whole create, so writing it would
hand the user a config that cannot work. Dropping it can leave nothing
selected, which is why the empty case warns.

**The answer is checked, not just the choices.** `NeedsUpload` reads the
offered choices, which is everything the design describes — but the
free-text entry appends its fingerprint *after* the form closes, so it
never becomes a choice, and a key carried in from an existing config is
a choice with no public key behind it. Neither can be uploaded from
here. Both are now named in a warning when the account is known, because
the create they will fail reports "invalid key identifiers" and has been
seen blaming the hostname instead. They are named rather than dropped:
the user asked for them, and may be about to register them by hand.
