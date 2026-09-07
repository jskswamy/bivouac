# Architecture Decision Records

One file per significant decision — context, decision, consequences. Numbered
in the order they were made, not necessarily the order they're implemented.

These are point-in-time records, not living documentation: an ADR is amended
with a status note when reality moves on, never rewritten. For what the code
does *today*, read [`../architecture.md`](../architecture.md).

| ADR | Decision | Status |
|-----|----------|--------|
| [0001](0001-go-implementation.md) | Go, as a standalone open-source repo | Accepted |
| [0002](0002-multi-instance-model.md) | Multi-instance model | Accepted (its command list still names the removed `watch`) |
| [0003](0003-git-derived-instance-identity.md) | Instance identity derived from the git repo | Accepted |
| [0004](0004-nix-home-manager-provisioning.md) | Nix + home-manager provisioning, cloud-init as a thin trigger | Accepted |
| [0005](0005-module-based-package-composition.md) | Module-based package/flake composition | Accepted (its example predates the move to `cloudlab.pkl`) |
| [0006](0006-credentials-via-aide-secrets.md) | Claude Code credentials via aide's secrets system | Not implemented; premise lapsed |
| [0007](0007-command-surface.md) | Command surface — `up` owns the repo, `sync`/`download` are for everything else | Superseded by the git-aware sync design |
| [0008](0008-provider-abstraction.md) | Provider abstraction — DigitalOcean first, not DigitalOcean-only | Accepted |
| [0009](0009-general-secrets-via-sops-nix.md) | General instance secrets via sops-nix, age key derived from the instance's own SSH host key | Proposed; not implemented |
| [0010](0010-one-instance-per-repo.md) | One instance per repo — sessions scale, instances are not shared across projects | Accepted |

Design specs and delivery plans that postdate these ADRs live in
[`../superpowers/`](../superpowers/); the most recent, and the one that
supersedes ADR-0007, is
[`2026-09-05-git-aware-sync-design.md`](../superpowers/specs/2026-09-05-git-aware-sync-design.md).
