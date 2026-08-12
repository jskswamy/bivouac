# 0008: Provider abstraction — DigitalOcean first, not DigitalOcean-only

## Status

Accepted

## Context

The initial design targeted DigitalOcean specifically throughout: droplet
sizes, droplet images, the DO API for create/destroy/status. DigitalOcean is
where this starts, but it shouldn't be where it's architecturally stuck —
other cloud providers are a reasonable future direction, and retrofitting a
provider boundary after instance identity, state, and the command surface
are all written assuming one specific API would mean touching most of the
codebase.

## Decision

Instance identity (ADR-0003), templates, state, and the command surface
(ADR-0007) are all provider-independent. Only VM lifecycle mechanics —
create, destroy, get, list, and running cloud-init on boot — sit behind a
`Provider` interface. `digitalocean` (via `godo`) is the only implementation
built now.

Provider-specific concepts (DO's droplet sizes/regions/images; whatever the
equivalent is elsewhere) stay as provider-specific spec fields rather than
being forced into a shared, generic vocabulary — there's no meaningful
common model across providers worth designing until a second one actually
exists to design against.

## Consequences

- Every instance's state record carries which provider created it, so
  multi-provider `list`/`status`/`down` route correctly without the CLI
  needing provider-specific logic outside the `Provider` implementation
  itself.
- Adding a second provider means writing a new `Provider` implementation and
  its spec fields; it does not mean touching instance identity, templates,
  package/flake composition, or the command surface.
- No second provider is implemented yet, and none is planned until there's
  an actual reason to build one — this ADR documents the seam, not a
  multi-cloud roadmap.
