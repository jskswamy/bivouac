# SSH key discovery for the wizard's `sshKeys` question

Status: designed, not implemented
Date: 2026-09-13
Tracks: `cloudlab-pwg.4` (this design)
Builds on: `docs/superpowers/specs/2026-09-09-cloudlab-init-and-presets-design.md`

## The problem

`sshKeys` is the one field in the schema whose wrong value is both
accepted and expensive.

Today it takes a DigitalOcean key ID or fingerprint that the user must
have registered by hand, and must then recall from memory — nothing in
cloudlab ever reads a local public key or asks DigitalOcean what the
account holds. `cloudlab init` inherited that unchanged: the question is
a free-text input, and it will take whatever is typed.

Every other required field fails loudly and early. A bad `region` or
`size` is rejected by `Droplets.Create` before anything is billed; a bad
`packages` entry is now caught by validation on the instance before the
switch. A bad `sshKeys` **succeeds**: the droplet is created, it boots,
it is billed, and it has no key you hold. The failure surfaces as a
refused login, several minutes and one instance later, and the only cure
is `down` and start again.

That asymmetry is the whole justification for this design. The 2026-09-09
spec deliberately refused to fetch `region`/`size` choices from the
provider, because "a flow that cannot start without credentials is a
worse first experience than one that checks the value when it is used."
That reasoning still holds for those two fields, and it is not weakened
here — what changes is that for `sshKeys` there is no later point at
which the value gets checked at all.

## The observation that shapes everything

**A correct `sshKeys` value can be produced with no credentials and no
network.**

DigitalOcean's `Key.Fingerprint` is the RFC 4716 MD5 presentation —
colon-separated hex, no prefix. `x/crypto/ssh.FingerprintLegacyMD5`
returns exactly that string, and `x/crypto` is already a direct
dependency. So given `~/.ssh/id_ed25519.pub`, cloudlab can compute the
fingerprint DigitalOcean will match on, locally, offline, in about five
lines.

The API is therefore not needed to *answer* the question. It is needed
only to answer two follow-ups: is this key actually on the account, and
if not, shall I put it there? That collapses what looked like a
credentials-gated feature into an offline one with two optional
enhancements.

## Decision

The `sshKeys` question becomes a multi-select over discovered keys,
in three tiers that degrade independently.

| Tier | Needs | Offers |
|---|---|---|
| 1 | nothing | local `~/.ssh/*.pub`, by comment and fingerprint |
| 2 | a token with SSH-key read | marks which are registered; lists account keys with no local half |
| 3 | a token with SSH-key create | offers to register a local key that is not on the account |

A free-text entry stays available at every tier. It is today's behaviour,
it is the only route for a key that exists on neither side, and removing
it would make the wizard less capable than the file it writes.

Tier 1 alone produces a correct value for the common case — a developer
whose key is already on their DigitalOcean account — which is precisely
the case the current question handles worst, because that user has the
right key sitting on disk and still has to go and look its fingerprint
up.

### The token is never reached for implicitly

Tier 2 and 3 run only when `DIGITALOCEAN_TOKEN` is set in the
environment, or when the user explicitly picks "look up my account".

They must never trigger `resolveToken`'s fallback path on their own. That
path decrypts through sops and commonly through an age-plugin-yubikey
identity whose touch policy waits for physical presence, with no output
of its own — see `resolveToken`'s own comment, and `JoinTailscale`'s. A
form that silently blocks on a key nobody has been asked to touch is the
same failure as a form blocked on absent stdin, and the 2026-09-09 spec
already calls that indistinguishable from a hang.

So the rule is the strict one: the first run of `cloudlab init` on a
fresh machine completes with no credentials at all.

### Fingerprints are written, not IDs

`sshKeys` entries are written as fingerprints. The DO provider's
`sshKeys()` already accepts either — a numeric string becomes an ID,
anything else a fingerprint — so both work today.

The fingerprint identifies the key material; the numeric ID identifies a
row in one account. A fingerprint survives deleting and re-adding the key
on DigitalOcean, means the same thing in a second account, and can be
checked by hand against `ssh-keygen -lE md5 -f ~/.ssh/id_ed25519.pub`. An
ID can be none of those things, and reads as a magic number in a config
file.

### Discovery reads every `*.pub`

Tier 1 globs `~/.ssh/*.pub` rather than walking a fixed list.

This looks inconsistent with `reconcile`'s `defaultIdentityFiles`, which
deliberately tries only OpenSSH's five defaults, so it is worth naming
why the two differ. That list is narrow because every key offered to an
sshd is a separate authentication attempt against a `MaxAuthTries` budget
of six — an eleventh key there costs a connection. Listing choices in a
form spends no such budget: the cost of an extra entry is a line on
screen, and the cost of a missing one is the user falling back to typing
a fingerprint by hand, which is the thing this exists to avoid.

Public keys are public. Reading them needs no secrets and reveals
nothing; the private halves are never opened.

## Components

| Unit | Responsibility |
|---|---|
| `internal/sshkeys` | glob `~/.ssh/*.pub`, parse, fingerprint. No network, no provider. |
| `internal/provider`'s `KeyRegistry` | optional capability: list and create account keys |
| `internal/provider/digitalocean` | implements `KeyRegistry` over `godo.KeysService` |
| `internal/wizard` | the tiers as data; which choices exist given what is available |
| `cmd/init.go` | the multi-select form |

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

Not added to `Provider`. That interface is four lifecycle methods and its
own doc comment says provider-specific concepts stay out of it rather
than being forced into a cross-provider abstraction. Key management is
exactly such a concept — a provider that authenticates some other way
would have to stub it — and the wizard is the only caller, so a type
assertion at one call site is cheaper than a method every future provider
must implement to return "unsupported".

## Token scopes

Tier 2 needs the account's SSH keys readable; tier 3 needs them
creatable. This is the "whatever the SSH-key lookup uses" that
`cloudlab-24k` left open when it proposed narrowing the token to a
fine-grained PAT, and it is now answerable: droplet create/read/delete,
plus SSH-key read, plus SSH-key create only if tier 3 is wanted.

A user who scopes a token without the key scopes loses tiers 2 and 3 and
keeps tier 1. The design must treat a 403 on `ListKeys` as "this tier is
unavailable", not as an error — a narrowly scoped token is the
recommended configuration, and it must not make `init` fail.

## Testing

- Fingerprinting is a pure function over a `.pub` file's bytes: table
  test against keys with known fingerprints, including a key with a
  comment, one without, and a file that is not a public key at all.
- Discovery against a temp `$HOME/.ssh` holding a mix of `.pub` files,
  private keys, and unrelated files.
- Tier selection is a pure function of (local keys, account keys or
  nil, create-capable or not): table test, no provider and no form.
- `KeyRegistry` against a fake, including the 403 that must degrade to
  tier 1 rather than fail.
- The forms are covered by the units beneath them, not driven — as in
  the 2026-09-09 spec.

## Rejected alternatives

**Require a token for the `sshKeys` question.** It is the first question
of the first run on a new machine. Gating it on credentials — worse, on
credentials that may sit waiting for a YubiKey touch — would make the
wizard unusable in exactly the situation it exists for.

**Read the account's keys and skip local discovery.** Simpler, and wrong
for the case that matters: it needs a token to answer at all, and it
cannot tell which of the account's keys the user actually holds the
private half of. A key on the account that is not on this machine
produces the same unreachable droplet as a typo.

**Compute SHA256 fingerprints.** What `ssh-keygen -l` prints by default
today, and what users will recognise — but not what DigitalOcean matches
on. The value has to be the one the API accepts; showing SHA256 next to
it in the form is a presentational choice, not a storage one.

**Generate a key when none is found.** A tempting fourth tier, rejected:
creating key material is a decision with consequences well outside
cloudlab, and a wizard that quietly adds an SSH key to someone's `~/.ssh`
has exceeded its remit. Say none were found and point at `ssh-keygen`.

**Verify the key on every `up`.** Would catch a fingerprint that stopped
being valid after the file was written. Rejected for now: it puts an API
call on the hot path of the command most likely to be scripted, to guard
against a case — deleting a key from DigitalOcean while leaving it in
`base.pkl` — that is rare and self-announcing the moment you try to log
in. Worth revisiting if it turns out not to be rare.
