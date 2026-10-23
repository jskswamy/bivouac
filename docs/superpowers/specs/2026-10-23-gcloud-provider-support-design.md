# Google Cloud Provider Support Design

## Context and Intent

Bivouac provisions ephemeral remote coding-agent virtual machines. Today,
DigitalOcean is the sole cloud provider implementation. However, users with
Google Cloud credits (such as monthly credits bundled with Google AI Ultra
subscriptions) or existing GCP infrastructure need to run Bivouac sessions on
Google Compute Engine (GCE) with the exact same workflow, speed, and safety as
DigitalOcean.

ADR-0008 ("Provider abstraction — DigitalOcean first, not DigitalOcean-only")
established the architecture for multi-cloud support: VM lifecycle mechanics
sit behind the `provider.Provider` interface, while instance identity, state
tracking, templates, and the command surface remain provider-agnostic. This
project implements Google Cloud (`gcloud`) as the second provider.

## Goals

1. **Identical command surface**: `bivouac up`, `down`, `status`, `list`, `ssh`,
   `session`, `sync`, and `herdr` work identically regardless of whether the
   underlying VM is running on DigitalOcean or Google Cloud.
2. **Provider-specific configuration**: Support Option 1 (`gcloud` and
   `digitalocean` blocks in `bivouac.pkl`) while retaining 100% backward
   compatibility for existing configs using top-level `region`/`size`.
3. **No `gcloud` CLI dependency**: All GCP API operations, authentication, and
   IAP tunneling are implemented directly in Go; Bivouac never shells out to
   the `gcloud` CLI.
4. **Service account authentication**: Authenticate using GCP Service Account
   keys via environment variables (`GOOGLE_APPLICATION_CREDENTIALS` or
   `GCLOUD_SERVICE_ACCOUNT_KEY`) or encrypted secrets (`gcloud_service_account_key`
   in `secrets.yaml`).
5. **Universal IAP SSH transport**: Use Google Cloud Identity-Aware Proxy (IAP)
   TCP tunneling for bootstrap, `WaitReady`, and provisioning, working seamlessly
   on private-only instances without exposing port 22 to the public internet.
6. **Transparent OpenSSH abstraction**: When an instance has no public IP and
   is not on Tailscale, Bivouac configures its OpenSSH profile with an internal
   `ProxyCommand` (`bivouac proxy <instance>`), making direct `ssh bivouac-<name>`,
   `git`, and `rsync` work automatically without user intervention.
7. **Optional Tailscale handoff**: If `tailscale = true`, join the tailnet during
   provisioning and switch preferred host to Tailscale; otherwise, retain the
   transparent IAP tunnel for ongoing access.
8. **Custom images**: Support building and launching GCE custom images via
   Packer (`googlecompute` builder) through Bivouac's `ImageManager` interface.
9. **Offline contract testing**: Implement a local mock server (`gcpfake`) so the
   entire test suite passes offline without touching real GCP infrastructure.

## Non-Goals & Anti-Goals

- **No key generation/management in Bivouac**: Creating or downloading service
  account keys from GCP is external to Bivouac. Bivouac only consumes the key.
- **No artificial cross-cloud abstraction**: Do not force DO slugs (`s-2vcpu-4gb`,
  `nyc3`) and GCE types (`e2-standard-4`, `us-central1-a`) into generic names.
  Provider blocks keep provider-native terminology.
- **No project-wide metadata pollution**: SSH keys are attached to instance
  metadata at launch time rather than modifying project-wide metadata.

---

## Detailed Design

### 1. Configuration Schema (`internal/config/Config.pkl`)

`bivouac.pkl` gains an optional `provider` selector and dedicated provider
configuration classes:

```pkl
provider: "digitalocean" | "gcloud" = "digitalocean"

digitalocean: DigitalOceanConfig?
gcloud: GCloudConfig?

class DigitalOceanConfig {
  region: String?
  size: String?
  image: String = "ubuntu-24-04-x64"
}

class GCloudConfig {
  project: String(!isEmpty)
  zone: String(!isEmpty)
  machineType: String(!isEmpty)
  network: String = "default"
  subnetwork: String?
  externalIP: Boolean = false
  diskSizeGb: Int = 50
  image: String = "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64"
}
```

#### Backward Compatibility
When `provider` is omitted or set to `"digitalocean"`, top-level `region`,
`size`, and `image` populate `digitalocean` if the `digitalocean` block is not
explicitly defined. If `provider = "gcloud"`, `gcloud` must be present (or
merged from `~/.config/bivouac/base.pkl`).

---

### 2. Credentials & Authentication

GCP authentication follows Bivouac's established precedence:

1. **Environment variable** (highest precedence, for CI/unattended agents):
   - `GOOGLE_APPLICATION_CREDENTIALS` (path to service account JSON key file)
   - Or `GCLOUD_SERVICE_ACCOUNT_KEY` (raw JSON content or file path)
2. **Encrypted secrets file** (`~/.config/bivouac/secrets.yaml`, via sops):
   - `gcloud_service_account_key`: string containing either raw JSON credentials
     or an absolute/tilde path to a local key file.

Bivouac loads credentials using `google.golang.org/api/option` with
`option.WithCredentialsJSON(...)`.

---

### 3. State Tracking & Provider Routing (`internal/state`)

To allow commands like `bivouac down <name>` and `bivouac status <name>` to
route properly without requiring `bivouac.pkl`, `state.Record` gains:

```go
type Record struct {
    // ... existing fields ...
    Project string `json:"project,omitempty"`
}
```

- For DigitalOcean instances, `Project` is empty and `Region` holds the DO slug (`nyc3`).
- For Google Cloud instances, `Provider` is `"gcloud"`, `Project` holds the GCP project ID,
  and `Region` holds the GCE zone (`us-central1-a`).
- **Zone vs. Region naming**: GCE instances are placed in specific zones (e.g. `us-central1-a`).
  To keep `state.Record` lean and avoid redundant schema fields, `Record.Region` directly
  holds the GCE zone string (`us-central1-a`). Commands like `bivouac status` and `bivouac list`
  continue reading `Record.Region` without branching on provider type.
- `resolveProvider(ctx)` in `cmd/run_instance.go` is refactored to accept the
  target `state.Record` (or active configuration during `up`), instantiating
  either `digitalocean.Provider` or `gcloud.Provider` accordingly.

---

### 4. Provider Implementation (`internal/provider/gcloud`)

A new package `internal/provider/gcloud` satisfies:
- `provider.Provider` (`Create`, `Destroy`, `Get`, `List`)
- `provider.ImageManager` (lists, tags, and deletes custom images)

Using the official Go SDK:
`cloud.google.com/go/compute/apiv1` and `google.golang.org/genproto/googleapis/cloud/compute/v1`.

#### SSH Key Injection
Rather than requiring project-level metadata permissions, `provider.InstanceSpec`
passes the resolved public keys directly. During `Create`, GCE instance metadata
is populated with:
```
key: "ssh-keys", value: "<user>:<public-key-1>\n<user>:<public-key-2>"
```
GCE's guest environment automatically configures `~/.ssh/authorized_keys` for the
designated user.

#### VM Creation (`Create`)
- Sets instance name, machine type, zone, network, and subnetwork.
- Attaches boot disk: `initializeParams` with image family/ID and `diskSizeGb`.
- Sets metadata:
  - `user-data`: interpolated cloud-init script from `provisioning.RenderCloudInit`.
  - `ssh-keys`: formatted list of allowed SSH public keys.
- If `externalIP == false`: omits `accessConfigs` from network interface.
- Returns `provider.VM` containing instance ID, zone, machine type, and private
  (or public) IP.

#### VM Status & Teardown (`Get`, `List`, `Destroy`)
- `Get`: retrieves instance state, mapping GCE status to Bivouac status strings.
- `Destroy`: calls `instances.Delete(project, zone, instance)`.
- `List`: queries instances filtered by label `bivouac-managed = true`. When
  `bivouac list` is run outside a git repository with no local `bivouac.pkl`,
  it derives the target GCP project IDs directly from the recorded instances in
  `~/.local/share/bivouac/state.json`, calling `compute.instances.aggregatedList`
  for each distinct project that has active records.

---

### 5. Transparent Native IAP SSH Transport

Bivouac implements direct IAP WebSocket tunneling in Go:
`wss://tunnel.cloudproxy.app/v4/connect?project=...&zone=...&instance=...&port=22&interface=nic0`

1. **In-Process Dialing (`reconcile.Connect`)**:
   During `WaitReady` and `Reconcile`, Bivouac checks if direct TCP dialing to
   `vm.IP` is possible. If `record.Provider == "gcloud"` and the VM has no public
   IP (or direct TCP fails), it establishes an authenticated IAP WebSocket stream
   wrapped as a `net.Conn`.
2. **Transparent OpenSSH Bridging (`bivouac proxy`)**:
   For external tools (`ssh`, `git`, `rsync`, `herdr`), Bivouac introduces an
   internal helper command:
   `bivouac proxy <instance-name>`
   In `internal/sshidentity/openssh.go`, if an instance is private without Tailscale,
   it renders:
   ```sshconfig
   Host bivouac-<name>
     ProxyCommand bivouac proxy <name>
     User <user>
     IdentityFile ...
   ```
   When the user runs `bivouac ssh <name>`, `ssh bivouac-<name>`, or `git fetch`,
   OpenSSH spawns `bivouac proxy <name>`, which connects stdio to the IAP WebSocket
   stream. All commands work transparently without extra steps.
3. **Tailscale Handoff**:
   If `tailscale = true`, once Tailscale is joined, Bivouac updates
   `record.PreferredHost` to the Tailscale IP/FQDN. The SSH profile switches to
   direct HostName routing over Tailscale, bypassing the IAP proxy for faster local
   mesh performance.

---

### 6. Images and Packer Integration

`internal/image/bundle` templates are extended to support GCP:

1. **Packer Template**:
   Generates a bundle with `source "googlecompute" "image"`:
   ```hcl
   source "googlecompute" "image" {
     project_id   = var.project_id
     zone         = var.zone
     source_image = var.source_image
     ssh_username = "root"
     metadata = {
       user-data = file("scripts/bootstrap.sh")
     }
     image_name   = "bivouac-${var.tag}-${formatdate("YYYYMMDD-hhmmss", timestamp())}"
     image_labels = {
       bivouac-image   = "true"
       bivouac-format  = "1"
       bivouac-arch    = var.arch
       bivouac-region  = var.zone
       bivouac-shell   = var.shell
       bivouac-tag     = var.tag
     }
   }
   ```
2. **Reuse Provisioning Scripts**:
   `wait-bootstrap.sh`, `build-closure.sh`, and `sanitize.sh` execute inside the VM
   and remain unchanged.
3. **Marker Resolution**:
   `internal/image/markers.go` parses GCE image labels into `Markers` to verify
   architecture, zone, shell, and tag eligibility.
4. **Disk Size Checks (`SizeDiskGB`)**:
   On DigitalOcean, size slugs (`s-1vcpu-1gb`) determine disk size. On Google Cloud,
   machine types have no fixed disk size; disk capacity is set independently by
   `diskSizeGb`. GCE's `SizeDiskGB(sizeSlug)` implementation evaluates against
   the configured `diskSizeGb` (defaulting to 50 GB) so that `image.Resolve` disk
   validation passes correctly.
5. **Packer Credential Hand-off**:
   Packer's `googlecompute` builder runs as a separate subprocess and cannot decrypt
   `secrets.yaml` directly. When credentials come from `secrets.yaml` (rather than
   an existing `GOOGLE_APPLICATION_CREDENTIALS` environment variable), `bivouac image build`
   decrypts the Service Account key just-in-time into a temporary file with `0600`
   permissions and sets `GOOGLE_APPLICATION_CREDENTIALS` for the Packer child process,
   securely unlinking the temporary file immediately upon exit.

---

### 7. Testing Strategy

1. **`internal/provider/gcloud/gcpfake`**:
   An `httptest.Server` implementing the Compute Engine REST API (instances, disks,
   images) and the IAP WebSocket tunnel protocol.
2. **Contract Tests**:
   `cmd/` and `internal/provider/gcloud` unit/contract tests execute against
   `gcpfake` without network access or live credentials.
3. **End-to-End Suite**:
   Validates full `up`, `down`, `status`, and `image` workflows through the fake
   harness.

---

## What is Left to the Builder

- Exact WebSocket binary subprotocol multiplexing (`proto`) implementation in `internal/provider/gcloud/iap`.
- Machine type and zone picker choices in `internal/picker`.
- Pricing estimation mapping for standard GCE instance families in `internal/lifecycle/cost.go`.
