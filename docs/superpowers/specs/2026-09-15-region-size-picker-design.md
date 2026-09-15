# A picker for region and size

Status: proposed
Date: 2026-09-15

## The problem

`region` and `size` are DigitalOcean slugs (`nyc3`, `s-1vcpu-1gb`) with no
mnemonic relationship to what they mean, and `wizard.PersonalQuestions()`
asks for both as plain free text. A user who does not already have these
memorized has to leave the wizard, look them up, and come back — and a
typo is a valid-looking slug the wizard cannot tell from a real one; it
surfaces only when `up` calls the provider.

This was a deliberate trade-off when the wizard first shipped (see
`2026-09-09-cloudlab-init-and-presets-design.md`): fetching the real list
needs a token, and a flow that cannot start without credentials was judged
worse than one that checks the value when it is used. That reasoning holds
for the *credential-free first run*, but it does not mean the token-present
case should stay free text too — when a token *is* available, cloudlab can
show the real options, and DigitalOcean's own size listing already comes
grouped by exactly the categories a reader would want (General Purpose,
CPU-Optimized, Memory-Optimized, Storage-Optimized, GPU Droplets).

This spec adds a picker for both fields, keeps free text as a first-class
escape at every step for someone who already knows the slug they want, and
— unlike the rest of the wizard — refuses outright when no working token
can be found, rather than letting the user answer blind and find out at
`up` that nothing could ever have been created.

## The flow

Region is asked first, because a region's size list is a fixed subset of
the account-wide catalog — DigitalOcean's own `Region.Sizes` field, not
something to filter after the fact by trial and error. Size is then
narrowed to what that region actually offers.

```
1. AskRegion   -> one region slug
                  picker: region name + slug, e.g. "New York 3 (nyc3)"
                  escape: "Type it myself" -> free text

2. AskSize     -> one size slug, scoped to the chosen region
   2a. category -> "General Purpose" / "CPU-Optimized" / "Memory-Optimized"
                    / "Storage-Optimized" / "GPU Droplets" (from each
                    size's own `Description`, not a hand-maintained list)
                    escape: "Type it myself" -> free text
   2b. size     -> slug + detail, e.g. "s-2vcpu-4gb (2 vCPU, 4GB, $24/mo)"
                    escape: "Type it myself" -> free text
```

Picking "Type it myself" at any step ends that field's question
immediately with a plain text entry — it does not fall back to asking
every remaining step as text too.

### Fail fast when there is no working token

A droplet cannot be created without a token, full stop — so unlike
`sshKeys` (where an unregistered or local-only key is still a usable
answer) there is no degraded-but-viable path for region/size to fall back
to. Continuing the wizard on a guess would only move the same, certain
failure from now to the moment `up` calls the provider, after the user has
also answered every project question in between.

So, only at the point this question would actually be asked (see
"Interaction with the ... repair" below — a fully-populated kept base
still asks nothing and touches no network), `askRegionAndSize` resolves
and validates a token *before* building any offer:

1. Resolve a token via `resolveToken` (`cmd/token.go`) — `DIGITALOCEAN_TOKEN`
   first, then the sops-encrypted secrets file. Unlike `askSSHKeys`'s
   deliberately conservative env-var-only check, this path accepts that it
   may trigger an age-plugin-yubikey touch prompt: the whole point here is
   to know for certain whether provisioning can work at all, and a token
   sitting only in secrets is a completely normal setup this codebase's
   own security model expects (see `docs/config.md`'s secrets handling).
2. Use it to call `ListRegions`. Any failure — no token found, the API
   reports the token invalid, or `provider.IsForbidden` (a token scoped
   without read access) — is fatal: return the error up through
   `askPersonal` → `settlePersonal` → `runInitFlowWith`, which surfaces it
   and exits before writing `base.pkl` or `cloudlab.pkl`, and before
   `settleProject` asks anything else.

The message names what to do: e.g. *"cloudlab needs a working DigitalOcean
token to look up regions and sizes: \<reason\>. Set `DIGITALOCEAN_TOKEN`,
or add `digitalocean_token` to your personal secrets file (`cloudlab
secrets edit`), then run `cloudlab init` again."* This mirrors
`requireTerminal`'s existing shape (name the exact problem, name the exact
command that fixes it) rather than inventing a new error style.

An empty `ListRegions`/`ListSizes` result from an otherwise-valid token is
treated the same way — it is still a token that cannot get the user a
provisionable region and size, which is the only thing this check cares
about. There is no successful-but-empty case to degrade gracefully from.

## Components

| Unit | Responsibility | Depends on |
|---|---|---|
| `provider.SizeRegistry` | `ListRegions`, `ListSizes` — mirrors `provider.KeyRegistry` | nothing |
| `digitalocean.Provider` | implements `SizeRegistry` via godo's `RegionsService`/`SizesService` | godo |
| `wizard.RegionOffer`/`wizard.SizeOffer` | choices as data, built from provider types — mirrors `wizard.KeyOffer` | `provider` |
| `prompter.AskRegion`/`AskSize` | two new interface methods; dynamic, dependent option lists don't fit the static `wizard.Question` model `Ask` already handles | `wizard` |
| `cmd/init_instance.go` (new) | token resolution + fail-fast validation, region→size filtering — mirrors `cmd/init_keys.go`'s shape, not its degrade-on-failure behaviour | `provider`, `wizard` |
| `cmd/prompt.go` | huh implementations of `AskRegion`/`AskSize`, each with the "type it myself" escape — mirrors `AskKeys` | `wizard` |

`wizard.PersonalQuestions()` drops its `region` and `size` entries (the
same way `sshKeys` is already absent from it, for the same reason: the
answer is discovered/composed, not a single typed value against a static
question). `askPersonal` (`cmd/init.go`) calls the new
`askRegionAndSize(ctx, out, p, sources, values)` alongside `askSSHKeys`,
in the same place `region`/`size` used to be answered by the generic
`ask()` loop.

### Interaction with the "kept base is missing a field" repair

`settlePersonal` currently has two shapes of behaviour: the "change" and
"nothing exists yet" branches always (re-)ask every personal question via
`askPersonal`; the "kept as-is" and "lifted from an old project file"
branches skip asking entirely, except for a repair pass added after an
earlier bug (a `base.pkl` missing a required field, e.g. `size`, silently
made it all the way to `config.Resolve`'s read-back before failing) that
asks only whichever of `wizard.PersonalQuestions()`'s fields `values`
still lacks.

Once `region`/`size` leave `PersonalQuestions()`, that repair pass no
longer sees them — reintroducing the exact bug it was written to close,
just for these two fields specifically. `askRegionAndSize` therefore takes
`values` directly (not two bare `current` strings) and asks only for
whichever of region/size is absent from it, so it does double duty:

- Called from `askPersonal` (`values` may already hold answers, e.g. the
  "change" branch's existing base) — behaves like today's `ask()`, i.e.
  every field is re-offered, pre-filled from `values`, because
  `askPersonal` is only reached when the user asked to answer these
  questions.
- Called once more from `settlePersonal`'s post-switch repair check,
  alongside the existing `missingQuestions` call — here it must ask
  *only* whatever `values` is missing, or "kept as-is" would start asking
  a fully-populated base's region and size on every run, which
  `TestInitFlow_ExistingBaseKept` already pins as a regression.

Concretely, `askRegionAndSize` takes a `forceAsk bool` (or equivalent):
true from `askPersonal`, false from the repair call. When `forceAsk` is
false and both fields are already present, it is a no-op — no token
resolution, no network call, no possibility of the fail-fast error above.
The token requirement only ever applies at the moment something is
actually about to be asked, which is also why a project that already has
a complete, working `base.pkl` never needs a DigitalOcean token just to
run `cloudlab init` for its `template`/`agents`/`packages` questions.

### Data shapes

```go
// provider
type SizeRegistry interface {
    ListRegions(ctx context.Context) ([]Region, error)
    ListSizes(ctx context.Context) ([]Size, error)
}
type Region struct {
    Slug, Name string
    Sizes      []string // slugs offered in this region
}
type Size struct {
    Slug, Description string // Description: "General Purpose", "GPU Droplets", ...
    Vcpus, MemoryMB, DiskGB int
    PriceMonthly float64
}

// wizard
type RegionOffer struct{ Choices []RegionChoice }
type RegionChoice struct{ Slug, Name string }

type SizeOffer struct{ Categories []SizeCategory }
type SizeCategory struct {
    Name  string
    Sizes []SizeChoice
}
type SizeChoice struct {
    Slug string
    Vcpus, MemoryMB, DiskGB int
    PriceMonthly float64
}
```

Unlike `KeyOffer`, `RegionOffer`/`SizeOffer` are never built with empty
`Choices`/`Categories` — by the time one is constructed, `ListRegions`/
`ListSizes` already succeeded with a non-empty result, or `askRegionAndSize`
has already returned the fail-fast error instead.

### Token resolution is injectable, same as `keySources`

```go
// cmd/init_instance.go
type instanceSources struct {
    // registry resolves a token (env, then secrets) and returns a client
    // to query it, or the resolution/validation error.
    registry func(ctx context.Context) (provider.SizeRegistry, error)
}

func defaultInstanceSources() instanceSources {
    return instanceSources{registry: func(ctx context.Context) (provider.SizeRegistry, error) {
        token, err := resolveToken(ctx)
        if err != nil {
            return nil, err
        }
        return digitalocean.New(token), nil
    }}
}
```

Tests inject a stub `registry` the same way `init_keys_test.go` injects a
stub `keySources.registry` — no real token, no real network, no real sops
call, for every case: a working registry, a resolution error, and a
`ListRegions`/`ListSizes` call that fails once the registry is reached.

## Error handling

- Nothing to ask (both fields already present, not forced): no token
  resolution attempted, no network call, no error possible.
- Token resolves and `ListRegions`/`ListSizes` succeed with results: full
  picker, with the "type it myself" escape available at every step for a
  user who already knows the slug.
- Token cannot be resolved at all, resolves but is rejected by the API,
  is forbidden to list regions/sizes, or the lists come back empty:
  fatal. `askRegionAndSize` returns an error naming which of these
  happened and how to fix it; `runInitFlowWith` propagates it and exits
  before writing either file or asking any further question.
- Whatever *is* written (the success path only) is still read back
  through `config.Resolve` before the flow exits, as every field already
  is — a picker that produced a bad slug is exactly as visible as a
  mistyped one.

## Testing

- `provider`/`digitalocean`: `SizeRegistry` methods against a fake godo
  client, same shape as the existing `KeyRegistry` tests.
- `wizard`: building `RegionOffer`/`SizeOffer` from raw `Region`/`Size`
  slices is a pure function — table tests, including the
  Region.Sizes-filters-Size grouping and the empty-input case.
- `cmd`: `askRegionAndSize` driven through the `scriptedPrompter` and a
  stub `instanceSources.registry`, covering: token resolution fails
  (returns the fail-fast error, writes nothing); registry works but
  `ListRegions`/`ListSizes` errors or comes back empty (same fail-fast
  error); registry and lists both succeed (full picker reaches the
  prompter); the "type it myself" escape at both the region and the
  category step; `forceAsk=false` with both fields already present is a
  no-op that never touches `registry` at all; `forceAsk=false` with only
  one field present asks just that one (and does require/validate a
  token, since it is asking something).
- `cmd`: `TestInitFlow_ExistingBaseKept` and
  `TestInitFlow_ExistingBaseMissingSizeIsAsked` (added for the earlier
  missing-field bug fix) both still pass unmodified — the first proves
  a complete kept base asks nothing, the second proves an incomplete one
  is repaired, and neither should need to change for this feature to land.
- The huh implementations themselves stay untested, per this codebase's
  existing rule that the forms are the one part that resists driving.

## Out of scope

**Filtering by architecture (`arch`).** Some sizes are architecture-locked
in ways the API does not currently surface cleanly through this account's
token; cross-referencing `arch` against size availability is left for a
follow-up if it turns out to matter in practice.

**Live pricing beyond what `ListSizes` already returns.** No currency
conversion, no discount awareness — the number shown is exactly
`PriceMonthly`/`PriceHourly` as the API reports it.

## Rejected alternatives

**Reuse the generic `wizard.Question`/`Select`+`Freeform` machinery
(`runSelect`) as-is.** It works for `template` because its options are a
static `[]string` known at question-definition time. Region and size need
labels that differ from the value written (`"New York 3 (nyc3)"` displayed,
`"nyc3"` written) and a second, dependent level of options (size scoped to
the chosen region) — neither fits `Question.Options []string` without
either losing the human-readable labels or forcing a network call inside
a pure question-list function. Dedicated `AskRegion`/`AskSize` methods
keep the same "discovered, not declared" precedent `sshKeys` already
established, rather than stretching a model built for a different case.

**Degrade to free text when no working token can be found, matching how
`askSSHKeys` degrades to local-only keys.** SSH keys have a genuinely
usable degraded path — a fingerprint typed by hand, or a key this machine
holds but the account has not seen, both still work once registered.
Region and size do not: without a token, `up` cannot create anything no
matter how correct the typed slug is, so "asking anyway" only delays a
certain failure past every remaining wizard question. Rejected in favour
of failing at the one point the flow actually knows this.

**Keep the env-var-only token check `keySources` uses, rather than the
fuller `resolveToken`.** `askSSHKeys` avoids `resolveToken` specifically
to dodge a surprise yubikey-touch prompt, which is a reasonable trade when
the fallback (local-only keys) is fully usable. Once the design is "fail
outright with no working token", the trade-off flips: refusing to even
check secrets would block every user whose token lives only in sops — a
setup this codebase's own security model treats as normal — for a problem
(an unexpected touch prompt) that is far smaller than being unable to run
`cloudlab init` at all.

**Filter sizes to the region by intersecting `Size.Regions` client-side
instead of reading `Region.Sizes`.** Both fields exist on the DigitalOcean
API and should agree, but `Region.Sizes` is the one already scoped to a
single region without a second pass over the full size list, and it is
the field the flow needs immediately after `AskRegion` returns.
