# Google Cloud Platform Setup

This guide walks through configuring Google Cloud Platform (GCP) for Bivouac.
Bivouac provisions Compute Engine virtual machines and connects to them
securely using Google Cloud Identity-Aware Proxy (IAP) TCP forwarding, avoiding
open public SSH ports.

---

## 1. Prerequisites & Required APIs

Set your project identifier:

```bash
export PROJECT_ID="your-project-id"
gcloud config set project "$PROJECT_ID"
```

Enable the Compute Engine and Identity-Aware Proxy APIs:

```bash
gcloud services enable \
  compute.googleapis.com \
  iap.googleapis.com
```

---

## 2. Authentication & IAM Permissions

Bivouac requires permissions to manage Compute Engine VMs and open IAP tunnels:
- `roles/compute.instanceAdmin.v1` (create, start, stop, and destroy instances)
- `roles/iap.tunnelResourceAccessor` (connect through Cloud IAP TCP forwarding)

### Option A: Local Development via Application Default Credentials (ADC)

If you are running Bivouac locally with your personal Google account, grant the
roles to your user account and log in:

```bash
USER_EMAIL="you@example.com"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="user:$USER_EMAIL" \
  --role="roles/compute.instanceAdmin.v1"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="user:$USER_EMAIL" \
  --role="roles/iap.tunnelResourceAccessor"

gcloud auth application-default login
```

### Option B: Dedicated Service Account (Recommended for Agents & CI)

Create a dedicated service account and generate a key file:

```bash
gcloud iam service-accounts create bivouac-sa \
  --display-name="Bivouac Service Account"

SA_EMAIL="bivouac-sa@$PROJECT_ID.iam.gserviceaccount.com"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:$SA_EMAIL" \
  --role="roles/compute.instanceAdmin.v1"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:$SA_EMAIL" \
  --role="roles/iap.tunnelResourceAccessor"

# Generate a JSON key file
gcloud iam service-accounts keys create ~/bivouac-gcp-key.json \
  --iam-account="$SA_EMAIL"
```

### Credential Discovery Priority

Bivouac looks for GCP credentials in this order:

1. **`GOOGLE_APPLICATION_CREDENTIALS`** environment variable pointing to a service account JSON file.
2. **`GCLOUD_SERVICE_ACCOUNT_KEY`** environment variable (either a file path or raw JSON string).
3. **`gcloud_service_account_key`** in `bivouac secrets edit` (encrypted at rest with SOPS/age).
4. **Application Default Credentials (ADC)** located at `~/.config/gcloud/application_default_credentials.json`.

---

## 3. Firewall Rule for Cloud IAP

Cloud IAP routes inbound SSH traffic through Google's internal proxy block
`35.235.240.0/20`. Ensure an ingress firewall rule allows this on your VPC network:

```bash
gcloud compute firewall-rules create allow-iap-ssh \
  --network=default \
  --allow=tcp:22 \
  --source-ranges=35.235.240.0/20 \
  --description="Allow SSH ingress from Google Cloud IAP"
```

---

## 4. VPC Networking: External IP vs. Cloud NAT

Bivouac instances must be able to download Nix, home-manager packages, and apt
updates during provisioning. Google Cloud VPCs provide two ways to handle
outbound internet access:

### Option A: Ephemeral External IP (`externalIP = true`, Recommended)

GCP assigns a 1:1 NAT public IP to the VM.
- **Cost:** ~$0.005/hour while the VM is active (about $0.12/day). **Zero** data processing fees.
- **Security:** Inbound ports remain closed by the GCP firewall; SSH continues to route securely through Cloud IAP.
- **Lifecycle:** Automatically provisioned on `bivouac up` and destroyed on `bivouac down`.

Configure in `bivouac.pkl` or `~/.config/bivouac/base.pkl`:

```pkl
provider = "gcloud"

gcloud {
  project = "your-project-id"
  zone = "asia-south1-c"
  machineType = "e2-standard-4"
  externalIP = true
}
```

### Option B: Cloud NAT Gateway (`externalIP = false`)

If your security policy prohibits public IPs on VMs entirely, you must configure
a Cloud NAT gateway in the target region so private instances can reach the
internet.

- **Cost:** ~$0.0014/hour per active VM + **$0.045 per GiB** of processed egress traffic.
- **Lifecycle:** Cloud NAT is a shared regional resource and is **not** managed by Bivouac. You create it once per region.

#### Create Cloud NAT:

```bash
REGION="asia-south1"

# 1. Create a Cloud Router
gcloud compute routers create bivouac-nat-router \
  --network=default \
  --region="$REGION"

# 2. Add a Cloud NAT gateway to the router
gcloud compute routers nats create bivouac-nat \
  --router=bivouac-nat-router \
  --region="$REGION" \
  --auto-allocate-nat-external-ips \
  --nat-all-subnet-ip-ranges
```

#### Delete Cloud NAT (When finished with testing):

```bash
REGION="asia-south1"

# 1. Delete the NAT gateway
gcloud compute routers nats delete bivouac-nat \
  --router=bivouac-nat-router \
  --region="$REGION" \
  --quiet

# 2. Delete the Cloud Router
gcloud compute routers delete bivouac-nat-router \
  --region="$REGION" \
  --quiet
```

---

## 5. Preflight Validation

When `externalIP = false`, Bivouac checks the target region before creating
the VM. If no Cloud NAT exists on the VPC network, `bivouac up` halts
immediately to prevent creating an unreachable instance that would fail during
package installation.
