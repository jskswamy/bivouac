# 0009: General instance secrets via sops-nix, age key derived from the instance's own SSH host key

## Status

Proposed — exploring sops-nix as the primary approach before committing.
1Password Service Accounts is the evaluated fallback if this doesn't hold up
(see Consequences).

## Context

[ADR-0006](0006-credentials-via-aide-secrets.md) covers only Claude Code
auth: `SOPS_AGE_KEY` is injected into ephemeral `shell`/`ssh` sessions,
aide decrypts `ANTHROPIC_API_KEY` in-process, nothing is written to disk.
It explicitly scoped itself to that one credential and flagged the rest:
"Any other credential a user's workflow needs on the instance is a
separate, not-yet-designed concern."

Two things push past that scope:

- **More credentials.** GitHub access, HuggingFace tokens, and other model
  API keys are needed on the instance, not just Claude Code's key.
- **Long-running services break the session-scoped model.** Running an
  agent-harness orchestrator (e.g. reachable remotely over Tailscale,
  spawning Claude/Pi/OpenCode on demand) means a process that starts at
  boot/`up` and keeps running with no live SSH session ever providing
  `SOPS_AGE_KEY`. The per-session injection pattern has nothing to attach
  to in that case.

Options considered:

- **HashiCorp Vault** — rejected for the same reason as before: needs
  dedicated server infrastructure, which is exactly what this is trying to
  avoid.
- **1Password Service Accounts** — a real alternative. No dedicated server,
  works on an Individual account (not gated to Business/Teams), live
  revocation without re-provisioning, audit log of secret access. Set aside
  for now because it adds an external SaaS dependency, a network dependency
  at boot, and a new package (`op` CLI) to the shared home-manager module —
  `age`/`sops` are already there.
- **sops-nix** — the native secrets module for the Nix + home-manager
  pipeline cloudlab already provisions through ([ADR-0004](0004-nix-home-manager-provisioning.md)).
  No new dependency, no network call, decrypts at activation time rather
  than per-session — which is exactly the property the long-running-service
  case needs.

Separately: placing an age *private* key on a freshly provisioned,
ephemeral VM securely was an open question. Transmitting one (via cloud-init
`user_data` or otherwise) means a secret-bearing provisioning channel that
doesn't exist today.

## Decision

- Adopt sops-nix for all instance-side secrets beyond Claude Code. GitHub,
  HuggingFace, and model API keys join the existing sops/age-encrypted file
  used by aide, decrypted at activation time instead of per-SSH-session.
- Long-running services get their secrets via a systemd
  `EnvironmentFile`/`LoadCredential` populated by sops-nix at activation.
  Any process that service spawns (e.g. an orchestrator launching an agent
  harness) inherits the same environment — no per-harness wiring.
- GitHub access goes through SSH agent forwarding instead of a stored
  token where possible: no GitHub secret to manage or rotate at all.
- **Age key placement: don't transmit a private key.** Use
  `sops.age.sshKeyPaths` pointing at the instance's own
  `/etc/ssh/ssh_host_ed25519_key`, which cloud-init already generates
  locally at first boot. As part of `up`, over the SSH connection already
  opened for provisioning, fetch the public half of that host key, convert
  it to an age recipient with `ssh-to-age`, add it to `.sops.yaml`, and
  re-encrypt (`sops updatekeys`) before pushing the file in via the
  existing rsync step. Nothing secret crosses the network in either
  direction — only the public key travels, VM → local.

## Consequences

- Every instance recreation (`down` + `up`) rotates the host key, which
  rotates the age recipient. The re-encrypt step must be automatic as part
  of `up`, not a manual step someone has to remember.
- Requires cloudlab to fetch and trust the instance's SSH host key during
  provisioning — infrastructure it needs anyway for real host-key
  verification (currently blind TOFU, not yet designed) and should be built
  once as part of this, not twice.
- Decrypted secrets land in tmpfs (`/run/secrets/...`), preserving
  ADR-0006's "no plaintext persists on VM disk" invariant.
- No new external dependency or paid tier: `age`/`sops` are already in the
  shared home-manager module (per the README); `ssh-to-age` is a small
  addition to it.
- If secret rotation/revocation on a long-lived instance proves too manual
  in practice, or an audit trail becomes a real need, 1Password Service
  Accounts is the fallback — recorded here so the comparison doesn't need
  re-deriving from scratch.
