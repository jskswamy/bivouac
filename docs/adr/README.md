# Architecture Decision Records

One file per significant decision — context, decision, consequences. Numbered
in the order they were made, not necessarily the order they're implemented.

| ADR | Decision |
|-----|----------|
| [0001](0001-go-implementation.md) | Go, as a standalone open-source repo |
| [0002](0002-multi-instance-model.md) | Multi-instance model |
| [0003](0003-git-derived-instance-identity.md) | Instance identity derived from the git repo |
| [0004](0004-nix-home-manager-provisioning.md) | Nix + home-manager provisioning, cloud-init as a thin trigger |
| [0005](0005-module-based-package-composition.md) | Module-based package/flake composition |
| [0006](0006-credentials-via-aide-secrets.md) | Claude Code credentials via aide's secrets system |
| [0007](0007-command-surface.md) | Command surface — `up` owns the repo, `sync`/`download` are for everything else |
| [0008](0008-provider-abstraction.md) | Provider abstraction — DigitalOcean first, not DigitalOcean-only |
