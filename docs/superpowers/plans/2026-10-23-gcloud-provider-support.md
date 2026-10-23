# Google Cloud Provider Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable users to provision, manage, connect to, and build custom images for ephemeral development VMs on Google Compute Engine (GCE) using GCP Service Account credentials and native Go IAP tunneling, maintaining identical workflows to DigitalOcean.

**Architecture:** A new `internal/provider/gcloud` package implements `provider.Provider` and `provider.ImageManager` using `cloud.google.com/go/compute/apiv1`. `Config.pkl` gains dedicated provider blocks with legacy fallback. A native IAP WebSocket dialer powers both in-process SSH/reconcile connections and an internal `bivouac proxy` command for OpenSSH `ProxyCommand` bridging. `internal/image` extends Packer generation with the `googlecompute` builder, while `internal/provider/gcloud/gcpfake` provides offline contract testing.

**Tech Stack:** Go 1.26, `cloud.google.com/go/compute/apiv1`, `google.golang.org/api/option`, `nhooyr.io/websocket` (or standard `golang.org/x/net/websocket`), Pkl (`pkl-go`), OpenSSH, Packer (`googlecompute` plugin).

**Spec:** `docs/superpowers/specs/2026-10-23-gcloud-provider-support-design.md`

## Global Constraints

- Tests first in every task: write the failing test, run it, observe the failure, implement, see it pass.
- Run Go tests redirected, never piped: `go test ./... > /tmp/out.log 2>&1; echo $?`.
- Shell out to `packer`, `sops`, `tailscale`, `nix`, `git`, `bd`; never link them as Go libraries. Cloud provider APIs use official Go SDKs (`godo` for DO, `compute/apiv1` for GCP).
- Commit messages: imperative, capitalized subject of ≤50 chars, no type prefix, no period; body wrapped at 72 columns explaining why; **never add an AI co-author trailer**.
- No real network calls or paid GCP calls in tests; all tests use `gcpfake` or fake HTTP endpoints.
- No `gcloud` CLI dependency anywhere; all GCP API calls and IAP tunnels run in native Go.

## Review Focus

1. **Missing GCP project when running `bivouac down` or `status` outside repo**: Handled by storing `Project` in `state.Record`.
2. **`SizeDiskGB` validation failure on custom GCE images**: Handled by returning configured `diskSizeGb` rather than looking up nonexistent machine type disk.
3. **Packer failing to authenticate when service account key is encrypted in `secrets.yaml`**: Handled by just-in-time temporary file generation and `GOOGLE_APPLICATION_CREDENTIALS` export.
4. **WebSocket connection drops during long Nix provisions over IAP**: Handled by keepalive pings and automatic reconnection in the IAP `net.Conn` wrapper.
5. **OpenSSH trying to dial unreachable RFC1918 private IP directly**: Handled by automatically rendering `ProxyCommand bivouac proxy <name>` in `ssh_config` when an instance is private without Tailscale.

---

### Task 1: Configuration Schema & Provider Resolution

**Files:**
- Modify: `internal/config/Config.pkl`
- Modify: `internal/config/Config.pkl.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/resolved.go`
- Test: `internal/config/config_test.go`
- Test: `internal/config/read_test.go`

**Interfaces:**
- Consumes: Pkl configuration parser.
- Produces: `config.Resolved` with `Provider string`, `DigitalOcean *DigitalOceanConfig`, `GCloud *GCloudConfig`, and backward-compatible `Region`/`Size`.

- [x] **Step 1: Write failing config tests for provider blocks and backward compatibility**
- [x] **Step 2: Run tests to verify failure**
- [x] **Step 3: Update `Config.pkl` and `resolved.go`**
- [x] **Step 4: Run tests to verify they pass**
- [x] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "Add provider configuration schema for Google Cloud"
```

---

### Task 2: State Record Project Support & Provider Factory

**Files:**
- Modify: `internal/state/state.go`
- Modify: `internal/state/state_test.go`
- Modify: `cmd/run_instance.go`
- Test: `cmd/lookup_test.go`

**Interfaces:**
- Consumes: `state.Record`.
- Produces: `state.Record.Project string`, and `resolveProviderFor(ctx, providerName, project, zone)` in `cmd/run_instance.go`.

- [ ] **Step 1: Write failing test for state Record Project serialization**

In `internal/state/state_test.go`:
```go
func TestRecord_ProjectField(t *testing.T) {
    rec := Record{Name: "myinst", Provider: "gcloud", Project: "test-proj", Region: "us-central1-a"}
    // assert json serialize / deserialize preserves Project
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/state -run TestRecord_ProjectField > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: FAIL.

- [ ] **Step 3: Implement Project field in `state.Record` and refactor provider resolution**

Add `Project string json:"project,omitempty"` to `state.Record`. Update `resolveProvider` in `cmd/run_instance.go` to support dispatching by record or active config.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/state ./cmd > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/state/ cmd/run_instance.go
git commit -m "Support GCP project field in instance state records"
```

---

### Task 3: GCP Fake Server (`internal/provider/gcloud/gcpfake`)

**Files:**
- Create: `internal/provider/gcloud/gcpfake/gcpfake.go`
- Create: `internal/provider/gcloud/gcpfake/routes.go`
- Create: `internal/provider/gcloud/gcpfake/gcpfake_test.go`

**Interfaces:**
- Consumes: `net/http/httptest`.
- Produces: `gcpfake.Server` providing GCE REST mock endpoints (`/compute/v1/projects/.../instances`, `zones`, `disks`, `images`) and test URL options.

- [x] **Step 1: Write failing test for `gcpfake.Server` lifecycle**
- [x] **Step 2: Run test to verify failure**
- [x] **Step 3: Implement mock HTTP handlers for GCE instances API**
- [x] **Step 4: Run test to verify it passes**
- [x] **Step 5: Commit**

```bash
git add internal/provider/gcloud/gcpfake/
git commit -m "Add mock Google Compute Engine server for testing"
```

---

### Task 4: Google Cloud Provider Implementation (`internal/provider/gcloud`)

**Files:**
- Create: `internal/provider/gcloud/gcloud.go`
- Create: `internal/provider/gcloud/options.go`
- Create: `internal/provider/gcloud/gcloud_test.go`

**Interfaces:**
- Consumes: `provider.InstanceSpec`, `gcpfake.Server`.
- Produces: `gcloud.Provider` satisfying `provider.Provider` (`Create`, `Destroy`, `Get`, `List`).

- [x] **Step 1: Write failing test for GCloud Provider Create/Get/Destroy**
- [x] **Step 2: Run test to verify failure**
- [x] **Step 3: Implement `Provider` in `internal/provider/gcloud/gcloud.go`**
- [x] **Step 4: Run test to verify it passes**
- [x] **Step 5: Commit**

```bash
git add internal/provider/gcloud/
git commit -m "Implement Google Cloud provider VM lifecycle"
```

---

### Task 5: Native IAP WebSocket Tunneling & `bivouac proxy` Command

**Files:**
- Create: `internal/provider/gcloud/iap/dialer.go`
- Create: `internal/provider/gcloud/iap/dialer_test.go`
- Create: `cmd/proxy.go`
- Create: `cmd/proxy_test.go`

**Interfaces:**
- Consumes: GCP Service Account OAuth2 token, instance parameters.
- Produces: `iap.Dial(ctx, project, zone, instance, port) (net.Conn, error)` and `bivouac proxy <instance>` CLI command bridging stdin/stdout to the tunnel.

- [ ] **Step 1: Write failing test for IAP dialer against mock WebSocket endpoint**

In `internal/provider/gcloud/iap/dialer_test.go`:
```go
func TestDial_EchoOverIAP(t *testing.T) {
    wsServer := gcpfake.NewIAPWebSocketServer(t)
    conn, err := iap.DialWithEndpoint(context.Background(), wsServer.URL(), "proj", "zone", "inst", 22)
    // test Write / Read through WebSocket wrapper
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/provider/gcloud/iap > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: FAIL.

- [ ] **Step 3: Implement IAP binary subprotocol WebSocket framing and `bivouac proxy`**

Implement the multiplexed framing in `dialer.go` and the stdio passthrough command in `cmd/proxy.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/gcloud/iap ./cmd -run TestProxy > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/gcloud/iap/ cmd/proxy*
git commit -m "Add native IAP tunnel dialer and proxy command"
```

---

### Task 6: Transparent OpenSSH Profile Integration & In-Process Connect

**Files:**
- Modify: `internal/sshidentity/openssh.go`
- Modify: `internal/sshidentity/profiles.go`
- Modify: `internal/reconcile/ssh.go`
- Test: `internal/sshidentity/profiles_test.go`
- Test: `internal/reconcile/ssh_test.go`

**Interfaces:**
- Consumes: `sshidentity.Connection`.
- Produces: OpenSSH profiles rendering `ProxyCommand bivouac proxy <name>` for private IAP instances, and `reconcile.Connect` using IAP dialer when direct TCP dialing is not possible.

- [ ] **Step 1: Write failing test for OpenSSH config with IAP proxy**

In `internal/sshidentity/profiles_test.go`:
```go
func TestRenderProfile_IAPProxyCommand(t *testing.T) {
    conn := Connection{Name: "myvm", Alias: "bivouac-myvm", User: "dev", UseIAP: true}
    rendered, err := renderProfile(conn)
    // assert rendered contains "ProxyCommand bivouac proxy myvm"
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/sshidentity -run TestRenderProfile_IAPProxyCommand > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: FAIL.

- [ ] **Step 3: Implement OpenSSH proxy rendering and reconcile dialer hook**

Update `renderProfile` to emit `ProxyCommand` when `UseIAP` is true, and hook the dialer into `reconcile.Connect`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sshidentity ./internal/reconcile > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sshidentity/ internal/reconcile/
git commit -m "Configure transparent OpenSSH proxying for IAP instances"
```

---

### Task 7: GCP Custom Images & Packer Integration

**Files:**
- Create: `internal/provider/gcloud/images.go`
- Create: `internal/provider/gcloud/images_test.go`
- Modify: `internal/image/bundle.go`
- Modify: `internal/image/markers.go`
- Modify: `internal/image/resolve.go`
- Test: `internal/image/bundle_test.go`
- Test: `internal/image/resolve_test.go`

**Interfaces:**
- Consumes: `provider.ImageManager`.
- Produces: GCE image management (list, tag, delete), Packer bundle generation with `googlecompute` builder, and marker tag resolution.

- [ ] **Step 1: Write failing tests for GCE image manager and Packer bundle generation**

In `internal/provider/gcloud/images_test.go` and `internal/image/bundle_test.go`:
```go
func TestGCloud_ImageManager(t *testing.T) {
    // verify ListImages with label filtering, DeleteImage, SizeDiskGB returning diskSizeGb
}

func TestRenderBundle_GCloud(t *testing.T) {
    // verify rendered image.pkr.hcl uses source "googlecompute" "image"
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/provider/gcloud ./internal/image -run "TestGCloud_ImageManager|TestRenderBundle_GCloud" > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: FAIL.

- [ ] **Step 3: Implement GCE ImageManager and update bundle templates**

Implement `ImageManager` in `internal/provider/gcloud/images.go`, update `image.pkr.hcl.tmpl` for `googlecompute`, and adapt `markers.go` to parse GCE label maps.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/provider/gcloud ./internal/image > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/gcloud/ internal/image/
git commit -m "Support Google Compute Engine custom images and Packer bundle"
```

---

### Task 8: End-to-End CLI Contract Tests & Verification

**Files:**
- Create: `cmd/contract_gcloud_test.go`
- Modify: `cmd/up.go`
- Modify: `cmd/list.go`

**Interfaces:**
- Consumes: Full CLI command surface and `gcpfake.Server`.
- Produces: Contract test proving `bivouac up`, `status`, `list`, and `down` work end-to-end against GCE mock.

- [ ] **Step 1: Write failing end-to-end contract test**

In `cmd/contract_gcloud_test.go`:
```go
func TestContract_GCloudLifecycle(t *testing.T) {
    srv := gcpfake.NewServer(t)
    // Run up -> verify instance active -> status -> list -> down -> verify deleted
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./cmd -run TestContract_GCloudLifecycle > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: FAIL.

- [ ] **Step 3: Wire provider resolution in `up.go` and `list.go`**

Ensure `up.go` and `list.go` seamlessly route to `gcloud.Provider` when configured or recorded.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd -run TestContract_GCloudLifecycle > /tmp/out.log 2>&1; cat /tmp/out.log`
Expected: PASS.

- [ ] **Step 5: Run full test suite and pre-commit checks**

Run: `go test ./... > /tmp/out.log 2>&1; echo $?`
Expected: 0 (PASS).

- [ ] **Step 6: Commit**

```bash
git add cmd/
git commit -m "Add end-to-end contract tests for Google Cloud provider"
```
