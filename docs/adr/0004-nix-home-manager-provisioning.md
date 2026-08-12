# 0004: Nix + home-manager provisioning, cloud-init as a thin trigger

## Status

Accepted

## Context

The straightforward approach to provisioning is a cloud-init YAML that runs
`apt`/shell commands directly per template. That gets unwieldy fast once
there's more than one template (python, docker+minikube) plus a
default-installed toolset shared across all of them (Claude Code, aide): it
means either duplicating install logic per template in bash, or building a
templating layer on top of bash to de-duplicate it — neither of which is
particularly declarative or reproducible.

Nix + home-manager solves the same problem in a way that's already proven
out for machine setup generally: reproducibility, no `apt` drift, one config
language instead of ad hoc `runcmd` lists.

## Decision

- Cloud-init's job shrinks to a template-agnostic bootstrap: install Nix
  (Determinate Systems installer — non-interactive, flakes enabled), then
  run `nix run home-manager -- switch --flake <ref>#<template>`.
- All actual differentiation (Python toolchain vs. Docker+minikube, plus
  packages shared across every template: git, age, aide, Claude Code) lives
  in Nix flake outputs — a `python` and a `docker` home-manager profile in
  cloudlab's own flake (ADR-0001) — not in per-template bash/YAML.
- Cloud-init itself is a single payload, identical for every template,
  parameterized only by which profile to activate.

## Consequences

- Base VM image stays a plain Ubuntu image (Nix installs fine there); no
  NixOS-specific image is required.
- Adding a new template means adding a home-manager profile to the flake,
  not writing a new cloud-init script.
- Reconciling packages later (ADR-0005) becomes "re-run home-manager switch"
  rather than "re-run arbitrary shell commands," which is what makes
  idempotent, safe-to-repeat reconciliation from `up`/`shell` practical.
- GPU instances keep using the provider's GPU-ready base images (CUDA
  drivers pre-installed); the Nix layer is independent of and doesn't touch
  driver setup.
