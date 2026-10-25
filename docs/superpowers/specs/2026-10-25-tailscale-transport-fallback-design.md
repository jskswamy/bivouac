# Provider-Agnostic Tailscale and Base Transport Routing Design

## Context and Intent

Bivouac instances can be provisioned across cloud providers with two different
base network models:
1. **Public IP instances** (DigitalOcean droplets, or Google Cloud GCE VMs with
   `externalIP = true`), where the base transport is direct TCP to `<public-ip>:22`.
2. **Private Tunneled instances** (Google Cloud GCE VMs with `externalIP = false`),
   where `record.IP` is a VPC-internal RFC1918 address (`10.x.x.x`) that is never
   directly routable from the user's machine, and the base transport is an
   Identity-Aware Proxy (IAP) tunnel (`bivouac proxy <name>` / `iap.DialTarget`).

On top of either base model, an instance may optionally enable **Tailscale**
(`tailscale = true` in `bivouac.pkl` or `bivouac tailscale`).

Previously, Bivouac treated joining Tailscale as a permanent, one-way replacement
of the instance's transport (`rememberPreferredHost` overwrote `PreferredHost`
with the `100.x.y.z` tailnet address and set `UseIAP = false`). At the same time,
CLI commands (`ssh`, `tmux`, `sync`, `download`, `pair`, `status`, `session`,
`provision`, `down`) continued passing `record.IP` (`10.x.x.x` or the public IP)
into `instanceTransport` and `reconcile.Connect`. This produced five interacting
failures whenever `tailscale = true`:

1. `instanceTransport` saw `!connection.UseIAP && host != connection.PreferredHost`
   (`"10.160.0.9" != "100.91.219.40"`) and injected `-o HostName=10.160.0.9`
   onto the `ssh` command line, overriding the SSH profile and forcing OpenSSH to
   dial the unroutable GCE private IP `10.160.0.9`.
2. `rememberPreferredHost` cleared `UseIAP = false` as soon as the remote VM
   joined Tailscale, without checking whether the local machine had Tailscale
   running or could reach the tailnet IP, permanently removing `ProxyCommand
   bivouac proxy <name>` from `~/.config/bivouac/ssh/config`.
3. `reconcile.Connect` ignored `PreferredHost` and, because `UseIAP` was `false`,
   dialed `10.x.x.x:22` over plain TCP first — stalling for the full 10-second
   TCP timeout on every connection before falling back to `dialIAP`.
4. `runInstanceGit` probed `PreferredHost` (`100.x.y.z:22`) and then fell back to
   probing `PublicIP` (`10.x.x.x:22`) over plain TCP, failing with
   `neither preferred nor public instance SSH endpoint answered`.
5. `reconcile.Connect` recorded the VM's host key in `~/.ssh/known_hosts` under
   `10.x.x.x`, while the Tailscale profile set `HostKeyAlias 100.x.y.z` (and the
   IAP profile omitted `HostKeyAlias` altogether), causing non-interactive
   OpenSSH calls (`git`, `rsync`, `herdr`) to fail host key verification.

This design replaces that brittle state transition with a provider-agnostic
two-layer transport model where Tailscale is an opportunistic overlay that
automatically falls back to the instance's base transport on every connection,
with clear UX visibility into which route is active.

---

## Goals

1. **Two-layer transport model (Overlay $\rightarrow$ Base):** Every instance
   retains its **Base Transport** (`UseIAP` tunnel or direct `PublicIP` TCP)
   for its entire lifetime. Tailscale (`100.x.y.z`) is treated as a dynamic
   **Overlay Transport** that is preferred when reachable from the client
   machine right now, and transparently bypassed in favor of the Base Transport
   whenever unreachable.
2. **All connectivity combinations work seamlessly:**
   - Tailscale disabled (`tailscale = false`).
   - `bivouac up` with `tailscale = true` while local Tailscale is stopped or on
     a different tailnet.
   - Both local machine and remote VM on the same tailnet.
   - Local Tailscale disconnecting *after* `bivouac up`, and reconnecting later.
   - Remote `tailscaled` stopping or logging out.
3. **Every client path uses the same routing rules:**
   - In-process Go SSH (`reconcile.Connect`).
   - Bivouac-spawned external tools (`bivouac ssh`, `tmux`, `sync`, `download`,
     `pair`, `connect`, `session` git transfers, `herdr`).
   - Direct OpenSSH invocations (`ssh bivouac-<name>`, plain `git fetch`,
     Herdr background machine connections).
4. **Unified host-key verification:** Switching between Tailscale, IAP, and
   public IP never triggers an unknown-host prompt or host-key mismatch.
5. **Transparent UX:** Users can see which route is active (and when a fallback
   is in effect) in `bivouac status` and during command execution.

## Non-Goals & Anti-Goals

- **No Mosh over IAP:** Mosh (`getmoshi.app` via `bivouac pair`) uses UDP and
   cannot traverse TCP-only IAP tunnels; `bivouac pair` continues to run its SSH
   setup session over the active SSH route while advertising the chosen Tailscale
   or public IP in the QR code.
- **No noisy `bivouac proxy` output:** `bivouac proxy` is an OpenSSH
  `ProxyCommand` invoked under the hood by `git`, `rsync`, and `herdr`. It must
  never write progress noise to `stdout` (which carries the SSH wire protocol) or
  pollute `stderr` during scripted/JSON commands.

---

## Detailed Design

### 1. State & Connection Metadata (`internal/sshidentity`, `internal/state`)

#### Preserving `UseIAP` as Base Transport Identity
In `sshidentity.Connection`:
- `PublicIP` holds the provider-assigned IP (`203.0.113.x` on DigitalOcean /
  public GCE, or `10.x.x.x` on private GCE).
- `UseIAP` is `true` if and only if the instance's Base Transport is a GCP IAP
  tunnel (private GCE instance without external IP).
- `PreferredHost` holds the Tailscale IP (`100.x.y.z`) once Tailscale has been
  joined, or equals `PublicIP` when Tailscale is not joined.

**Crucial invariant:** `rememberPreferredHost` in `internal/lifecycle/transport.go`
updates `c.PreferredHost = host`, but **never sets `c.UseIAP = false`**. Likewise,
`state.Record.ConnectionContext` in `internal/state/state.go` sets `c.UseIAP = true`
for private `gcloud` records regardless of `r.TailscaleJoined`. Using
`isPrivateHost(c.PublicIP)` (RFC1918 `10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`) also automatically repairs existing `state.json` records (such
as an instance that already had `UseIAP` cleared when Tailscale joined) without
requiring the user to recreate the instance.

#### Shared Overlay Probe on `sshidentity.Connection`
`sshidentity.Connection` provides:
- `HasOverlay() bool`: returns `c.PreferredHost != "" && c.PreferredHost != c.PublicIP`.
- `DialOverlay() (net.Conn, error)` and `OverlayReachable() bool`: dials the
  overlay address on the SSH port with `OverlayProbeTimeout` (`2 * time.Second`),
  caching negative probe results in-process for a short TTL (`5 * time.Second`)
  so multi-step commands (`status`, `session start`, `up`) do not pay multiple
  2-second timeouts against the same unreachable tailnet IP.

---

### 2. Unified Host Key Alias & `known_hosts` (`internal/sshidentity`, `internal/reconcile`)

#### OpenSSH Profile Rendering (`internal/sshidentity/openssh.go`)
When `c.UseIAP` is `true` (whether or not Tailscale is also configured in
`c.PreferredHost`), `renderProfile` renders:

```sshconfig
Host bivouac-<name>
  ProxyCommand bivouac proxy <name>
  User <user>
  HostKeyAlias <normalized-PublicIP>
  IdentitiesOnly yes
  IdentityFile "<path>"
```

When `c.UseIAP` is `false` (public IP instance):
```sshconfig
Host bivouac-<name>
  HostName <host>
  Port <port>
  User <user>
  HostKeyAlias <normalized-PublicIP>
  IdentitiesOnly yes
  IdentityFile "<path>"
```
In **both** cases, `HostKeyAlias` is pinned to
`knownhosts.Normalize(net.JoinHostPort(publicHost, publicPort))` (i.e. `c.PublicIP`).
In `internal/sshidentity/profiles.go`, `checkUser` validates `hostkeyalias` for
both `c.UseIAP` and `!c.UseIAP` profiles against `c.PublicIP`.

#### `reconcile.Connect` Host Key Recording (`internal/reconcile/ssh.go`)
When `reconcile.Connect` performs the SSH handshake (`ssh.NewClientConn`), the
canonical address passed to `ssh.NewClientConn` for `HostKeyCallback`
verification is always normalized from `selected.PublicIP` (when `selected` is in
context) or `ip`. If `selected.HasOverlay()`, `trustOnFirstConnectCallback`
records the host key for both `selected.PublicIP` and `selected.PreferredHost` in
`~/.ssh/known_hosts`.

---

### 3. Shared Route Resolution & Dialing (`internal/reconcile`, `cmd/proxy.go`, `internal/lifecycle`)

#### 3a. In-Process Dialing (`reconcile.Connect` in `internal/reconcile/ssh.go`)
When `selected, ok := sshidentity.FromContext(ctx)` is present:
1. **Try Tailscale Overlay first (if configured):**
   If `selected.HasOverlay()`:
   - Call `selected.DialOverlay()`.
   - If it succeeds, use that `net.Conn`.
   - If it fails, emit a fallback progress notice and proceed immediately to Step 2.
2. **Dial Base Transport:**
   - If `selected.UseIAP` is `true`: call `dialIAP(ctx, selected, port)`
     **immediately**. Never dial `selected.PublicIP` (`10.x.x.x`) over plain TCP.
   - If `selected.UseIAP` is `false`: dial `selected.PublicIP` over TCP with
     `clientConfig.Timeout` (10s). If that fails and `selected.Project != ""`,
     fall back to `dialIAP`.

#### 3b. OpenSSH `ProxyCommand` (`bivouac proxy <name>` in `cmd/proxy.go`)
When OpenSSH invokes `bivouac proxy <name>`:
1. Load `record` from `state.Open()` and derive `c` from `record.ConnectionContext`.
2. **Try Tailscale Overlay first:**
   If `c.HasOverlay()`:
   - Call `c.DialOverlay()`.
   - If connected, bridge `stdin`/`stdout` directly to that TCP connection.
3. **Fall back to Base Transport:**
   - If `c.UseIAP` (or `record.Provider == "gcloud"` with private IP): dial
     `iap.DialTarget(ctx, record.Project, record.Region, record.Name, port)` and
     bridge `stdin`/`stdout`.
   - Otherwise: dial `net.JoinHostPort(record.IP, port)` over TCP and bridge
     `stdin`/`stdout`.

#### 3c. `instanceTransport` and `runInstanceGit` (`internal/lifecycle/transport.go`)
1. **`instanceTransport(ctx, host, user)`**:
   - When `connection.UseIAP` is `true`:
     - Ensure the managed SSH profile is up to date (`ensureManagedAlias`) and
       return `alias, nil, nil` (no `-o HostName=...` override), because
       `ProxyCommand bivouac proxy <name>` handles Tailscale-first $\rightarrow$
       IAP fallback dynamically.
   - When `connection.UseIAP` is `false` (Public IP instance):
     - If `connection.HasOverlay()`:
       - If `connection.OverlayReachable()`, return `alias, nil, nil` (using the
         profile's `HostName <PreferredHost>`).
       - If not reachable, return `sshEndpointOptions(connectionWithHost(connection, connection.PublicIP))`
         so OpenSSH falls back to the public IP.
2. **`runInstanceGit(ctx, localRepo, endpoint, args...)`**:
   - When `c.UseIAP` is `true`:
     - Skip the plain-TCP `reachable(fallback)` check when `c.PreferredHost` is
       unreachable, and omit `sshEndpointOptions(endpoint)` from
       `core.sshCommand` so OpenSSH uses the managed alias's `ProxyCommand
       bivouac proxy <name>` and `HostKeyAlias`.
   - When `c.UseIAP` is `false`:
     - Probe `c.PreferredHost` (`2s`), fall back to `c.PublicIP` (`2s`) if
       unreachable, and pass `sshEndpointOptions(endpoint)` with
       `HostKeyAlias` pinned to `c.PublicIP`.

#### 3d. `bivouac connect` Local Tailscale Reachability (`cmd/run_ports.go`)
In `runConnect`:
- When `record.TailscaleJoined` is `true` and `TailscaleIP` returns a `100.x.y.z`
  address from the remote instance, `runConnect` checks whether that `tailnetIP`
  is reachable from the local machine (`lifecycle.TailnetReachable(tailnetIP)`).
- If the local machine cannot reach `tailnetIP`, `runConnect` treats `tailnetIP`
  as empty (`""`) for `ConnectTarget` and `Forward`, forwarding the remote port
  to `http://localhost:<port>` over SSH instead of printing an unreachable
  `http://100.x.y.z:<port>` URL.

---

### 4. UX & Route Transparency

#### 4a. `bivouac status` (Human and `--json`)
`lifecycle.InstanceStatus` gains a `Route` summary (`lifecycle.RouteStatus`)
resolved during `status`:

| Instance Configuration & Live State | `bivouac status` `Route` Line | `status --json` `route` Object |
| :--- | :--- | :--- |
| Tailscale joined & reachable from this machine | `tailscale (100.91.219.40)` | `{"active": "tailscale", "address": "100.91.219.40", "fallback": "iap"}` |
| Tailscale joined (`100.91.219.40`), unreachable locally, private GCE | `iap tunnel (tailscale 100.91.219.40 unreachable)` | `{"active": "iap", "tailscaleAddress": "100.91.219.40", "tailscaleReachable": false}` |
| Tailscale joined (`100.91.219.40`), unreachable locally, public IP | `public ip 203.0.113.5 (tailscale 100.91.219.40 unreachable)` | `{"active": "public_ip", "address": "203.0.113.5", "tailscaleAddress": "100.91.219.40", "tailscaleReachable": false}` |
| Tailscale not joined, private GCE | `iap tunnel` | `{"active": "iap"}` |
| Tailscale not joined, public IP | `public ip (203.0.113.5)` | `{"active": "public_ip", "address": "203.0.113.5"}` |

#### 4b. Progress Reporting During Commands
1. **During `bivouac up` and `bivouac tailscale`:**
   After `TailscaleIP` retrieves the remote `100.x.y.z` address and
   `rememberPreferredHost` records it:
   - Probe `<tailnet-ip>:22` from the local machine (`2s`).
   - If reachable: report `routing via tailscale (100.91.219.40)`.
   - If unreachable: report
     `tailscale joined on instance (100.91.219.40), not reachable from this machine — using iap tunnel`
     (or `using public ip 203.0.113.5`).
2. **During interactive attach (`bivouac ssh`, `bivouac tmux`, `bivouac herdr`, `bivouac connect`):**
   Before handing off the terminal to `ssh` or `herdr`, when a Tailscale-joined
   instance falls back because local Tailscale cannot reach `PreferredHost:22`,
   print a concise notice via `cmd.ErrOrStderr()`:
   `→ tailscale (100.91.219.40) unreachable; connecting via iap tunnel` (or
   `via public ip 203.0.113.5`).
3. **During in-process `reconcile.Connect`:**
   When `reconcile.Connect` falls back from `PreferredHost` (`100.x.y.z`) to IAP
   or Public IP, emit a progress line via `provider.ReportProgress(ctx, ...)`.
