# Tailscale and Base Transport Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Tailscale an opportunistic overlay transport across all providers (GCE IAP and Public IP) that automatically falls back to the instance's base transport across Go SSH, OpenSSH CLI commands, `bivouac proxy`, git operations, `herdr`, and `connect`, with unified `HostKeyAlias` verification and route visibility in `status` and command progress.

**Architecture:** `sshidentity.Connection.UseIAP` remains `true` for the entire lifetime of private GCE instances (and self-heals from `state.Record.ConnectionContext` when `r.Provider == "gcloud"` and `c.PublicIP` is RFC1918 private). `sshidentity.Connection` owns the shared `HasOverlay()` and 2-second `DialOverlay()` / `OverlayReachable()` helpers (caching unreachable results per process for 5s to avoid repeated 2s stalls within one command), while `reconcile.Connect`, `bivouac proxy <name>`, and `instanceTransport`/`runInstanceGit` probe the overlay first and fall back to the base transport (`dialIAP` or `PublicIP:22`) with `HostKeyAlias` pinned to normalized `c.PublicIP`.

**Tech Stack:** Go (`golang.org/x/crypto/ssh`, `golang.org/x/crypto/ssh/knownhosts`, `cobra`)

**Spec:** `docs/superpowers/specs/2026-10-25-tailscale-transport-fallback-design.md`

## Global Constraints

- Never pipe `go test` output into `tail`, `tee`, or `grep`; always redirect to a file (`go test ./... > /tmp/out.log 2>&1`).
- Tailscale TCP reachability probe timeout is `2 * time.Second`.
- `bivouac proxy` must never write progress lines to `stdout` or `stderr` during normal operation.
- Commit messages use classic style: imperative subject $\le 50$ chars, capitalized, no period, no `type:` prefix, body wrapped at 72 chars explaining why, no AI `Co-Authored-By` trailer.

## Review Focus

1. Existing `state.json` and `profiles.json` records where `UseIAP` was previously overwritten to `false` when Tailscale joined (e.g. `Provider: "gcloud"`, `IP: "10.160.0.9"`, `TailscaleJoined: true`, `SSH.UseIAP: false`) must self-heal to `UseIAP: true` in `Record.ConnectionContext` AND update `profiles.json` + `~/.config/bivouac/ssh/config` via `ProfileStore.Upsert` when `ensureManagedAlias` runs.
2. `TestManagedAliasRejectsChangedHostKey` and `TestOrdinaryGitFetchPersistsSelectedIdentity` run real `ssh` in tests where `bivouac` is not on `$PATH` and `UseIAP == false`; `UseIAP == false` profiles must continue using `HostName` + `Port` + `HostKeyAlias` without `ProxyCommand`.
3. When `c.UseIAP == true` and `c.PreferredHost == "100.91.219.40"`, `instanceTransport(ctx, "10.160.0.9", user)` must return `alias, nil, nil` (not `-o HostName=10.160.0.9`).
4. When `c.UseIAP == true` and `c.PreferredHost == "100.91.219.40"` is unreachable locally, `runInstanceGit` must not fail with `neither preferred nor public instance SSH endpoint answered`; it must invoke `git` via the managed alias (`ProxyCommand bivouac proxy <name>`).
5. When `record.TailscaleJoined == true` on a private GCE instance and local Tailscale cannot reach `100.x.y.z:22`, `bivouac connect 8080` must fall back to SSH port forwarding (`localhost:8080`) rather than printing an unreachable `http://100.x.y.z:8080` URL.
6. Multi-connection commands (`bivouac status`, `bivouac session start`, `bivouac up`) must not pay multiple 2-second dead-Tailscale timeouts back-to-back for the same unreachable overlay endpoint within the same command run.

---

### Task 1: Preserve `UseIAP`, Unify `HostKeyAlias`, and Add Shared Overlay Probe in `internal/sshidentity` and `internal/state`

**Files:**
- Modify: `internal/sshidentity/context.go`
- Modify: `internal/sshidentity/openssh.go:81-115`
- Modify: `internal/sshidentity/profiles.go:220-250`
- Modify: `internal/state/state.go:52-67`
- Test: `internal/state/state_test.go`
- Test: `internal/sshidentity/profiles_test.go`

**Interfaces:**
- Produces:
  - `sshidentity.OverlayProbeTimeout = 2 * time.Second`
  - `(c Connection) HasOverlay() bool`
  - `(c Connection) OverlayTarget() (string, error)`
  - `(c Connection) DialOverlay() (net.Conn, error)`
  - `(c Connection) OverlayReachable() bool`
  - `state.Record.ConnectionContext(ctx context.Context) context.Context`
  - `renderProfile` and `checkUser` in `internal/sshidentity` with `HostKeyAlias` pinned to `c.PublicIP`.

- [ ] **Step 1: Write failing tests in `internal/state/state_test.go` and `internal/sshidentity/profiles_test.go`**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement `HasOverlay`/`DialOverlay`/`OverlayReachable`, `ConnectionContext` healing, and `HostKeyAlias` in IAP profiles**
- [ ] **Step 4: Run tests to verify they pass**

---

### Task 2: Two-Layer Dialing & Host Key Verification in `internal/reconcile` and `cmd/proxy.go`

**Files:**
- Modify: `internal/reconcile/ssh.go:92-190`
- Modify: `cmd/proxy.go:25-70`
- Test: `internal/reconcile/ssh_test.go`
- Test: `cmd/proxy_test.go`

**Interfaces:**
- Consumes: `sshidentity.Connection.HasOverlay()`, `DialOverlay()` from Task 1.
- Produces:
  - `reconcile.Connect(ctx, ip, user)` overlay-first then base-transport dialing + canonical `PublicIP` host key verification.
  - `bivouac proxy <name>` (`cmd/proxy.go`) overlay-first then base-transport (`iap.DialTarget` or TCP) bridging.

- [ ] **Step 1: Write failing tests in `internal/reconcile/ssh_test.go` and `cmd/proxy_test.go`**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement overlay-then-base dialing in `internal/reconcile/ssh.go` and `cmd/proxy.go`**
- [ ] **Step 4: Run tests to verify they pass**

---

### Task 3: Fix `instanceTransport`, `rememberPreferredHost`, `runInstanceGit`, and `bivouac connect`

**Files:**
- Modify: `internal/lifecycle/transport.go:17-235`
- Modify: `cmd/run_ports.go:85-135`
- Test: `internal/lifecycle/transport_test.go`
- Test: `cmd/ssh_workflow_test.go`

**Interfaces:**
- Consumes: `sshidentity.Connection` (`UseIAP`, `PublicIP`, `PreferredHost`, `HasOverlay()`, `OverlayReachable()`) from Task 1.
- Produces:
  - `rememberPreferredHost` preserves `c.UseIAP`.
  - `instanceTransport` and `runInstanceGit` support `UseIAP` + Tailscale and `!UseIAP` + Tailscale fallback.
  - `TailnetReachable(ip string) bool` in `internal/lifecycle/transport.go`, used by `cmd/run_ports.go` (`runConnect`).

- [ ] **Step 1: Write failing tests in `internal/lifecycle/transport_test.go` and `cmd/ssh_workflow_test.go`**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement `transport.go` and `cmd/run_ports.go` updates**
- [ ] **Step 4: Run tests to verify they pass**

---

### Task 4: Route Transparency in `bivouac status` and Command Progress Notices

**Files:**
- Modify: `internal/lifecycle/status.go`
- Modify: `internal/lifecycle/up_plan.go:100-140`
- Modify: `internal/lifecycle/tailscale.go:15-55`
- Modify: `cmd/status_print.go:24-120`
- Modify: `cmd/run_instance.go:115-135,255-282`
- Modify: `cmd/run_attach.go:15-100`
- Test: `internal/lifecycle/status_test.go`
- Test: `cmd/status_print_test.go`

**Interfaces:**
- Consumes: `sshidentity.Connection.HasOverlay()`, `OverlayReachable()`, `TailnetReachable` from Tasks 1 & 3.
- Produces:
  - `lifecycle.RouteStatus` struct on `lifecycle.InstanceStatus` and `lifecycle.ResolveRoute(record state.Record) RouteStatus` + `(r RouteStatus) Summary() string`.
  - `printStatus` and `printStatusJSON` in `cmd/status_print.go` include `Route` / `"route"`.
  - `up_plan.go`, `tailscale.go`, `cmd/run_instance.go` (`runSSH`, `runTmux`, `runStatus`), and `cmd/run_attach.go` (`runHerdr`) report route/fallback notices and reconcile the managed SSH profile on `status`.

- [ ] **Step 1: Write failing tests in `internal/lifecycle/status_test.go` and `cmd/status_print_test.go`**
- [ ] **Step 2: Run tests to verify they fail**
- [ ] **Step 3: Implement `RouteStatus`, `printStatus` / `printStatusJSON`, and attach progress notices**
- [ ] **Step 4: Run full test suite and linters**
