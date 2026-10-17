# Testing cloud images without DigitalOcean

Status: Draft 2026-10-17, decisions taken autonomously while the user was
away; each is recorded below for review. Companion to
[the cloud images design](2026-10-17-cloud-images-design.md).

## Why

Every DigitalOcean and Packer interaction in the cloud images feature was
tested against Go interface fakes above the provider layer, and the
provider layer alone against hand-written HTTP fixtures. Nothing ran the
real commands through the real client against an API, nothing ran the
real Packer DigitalOcean plugin, and nothing ran the builder scripts on a
real Ubuntu. The only way left to find out was a paid run, which is the
wrong place to discover bugs: each attempt costs a builder droplet and
twenty to forty minutes.

The goal is that a paid run is an acceptance check that passes first
time, not a debugging session.

## Tiers

| Tier | What runs | Where | Cost |
|---|---|---|---|
| 1. Contract | Real `bivouac` commands, real `digitalocean.Provider`, a stateful fake DigitalOcean API | `go test ./...`, every run | seconds |
| 2. End to end | Real `bivouac` binary, real Packer and DigitalOcean plugin, the same fake API, QEMU VMs standing in for droplets | opt-in: `BIVOUAC_E2E_QEMU=1 go test ./e2e/...` | 30-60 min, no cloud |
| 3. Acceptance | Everything real, on DigitalOcean | by hand, with a token | cents |

What only tier 3 can prove: token scopes, real API field quirks and
timing, DigitalOcean's own Ubuntu image and agent. Everything else should
fail in tier 1 or 2 first.

## Decisions

**D1. One stateful fake API, shared by tiers 1 and 2.** It lives in
`internal/provider/digitalocean/dofake`, an ordinary (non-test) package
so command tests and the tier 2 harness use the same code. It holds
droplets, images, actions, SSH keys, tags and sizes in memory and serves
them over `httptest`. *Alternative rejected:* per-test `ServeMux`
fixtures, which is what the provider tests already do; they cannot follow
a sequence (build, tag, list, prune) across calls.

**D2. Unknown routes fail the test.** Any request the fake does not
implement gets a 501 and is recorded; the test helper fails on any
recorded unknown request. Silence would let a new API call go untested.
A request without a bearer token gets a 401 and is not recorded: other
processes on a dev VM (seen on a bivouac session VM, most likely
tailscaled) send two bare `GET /` requests to every new localhost
listener, which failed whichever contract test owned the port, about
one run in six. The provider always sends a token, so nothing it sends
escapes D2.

**D3. Wire shapes come from godo's own types.** The fake marshals
`godo.Droplet`, `godo.Image`, `godo.Key` and friends inside DigitalOcean's
envelopes (`{"droplet": ...}`, `{"images": [...], "links": ..., "meta":
...}`) rather than hand-written JSON. That makes the fake agree with the
client by construction. *Known gap:* it agrees with godo's model of the
API, not necessarily the live API; the existing hand-written provider
fixtures and tier 3 cover the difference.

**D4. Failure injection is per route and per count.** A test can make
the next N calls to a method and path pattern return a status (403, 404,
429 with `Retry-After`, 500), delay a snapshot in `pending`, or run a
hook before a request (to model another actor deleting an image between
plan and delete).

**D5. Production code honours `DIGITALOCEAN_API_URL`.** Tier 2 runs the
real `bivouac` binary, so the base URL has to be configurable from
outside. The name is the one the Packer DigitalOcean plugin already reads
(confirmed in the v1.4.1 plugin binary), so one variable points both at
the fake. When set, bivouac prints the URL it is using to stderr, so it is
never silent. The token is already trusted from the environment, so this
adds no new trust boundary.

**D6. Tier 2 networking is a bridge, not port forwarding.** The host's
sshd listens on `0.0.0.0:22`, and both Packer and `bivouac up` connect to
a droplet's address on port 22. Each QEMU VM gets a tap device on a
dedicated bridge, `10.213.77.0/24` with NAT for the VM's internet access
(Nix and GitHub downloads), and a static address through cloud-init
network config. Chosen not to collide with this host's `10.47/16`,
`10.139/16` or `172.17/16`. Setup needs sudo, which the harness checks for
and skips with a clear message without it.

**D7. The fake emulates DigitalOcean's boot contract through NoCloud.**
For each created droplet the harness writes a NoCloud seed: the droplet's
`user_data` verbatim, meta-data with the droplet name as hostname, and
vendor data doing what DigitalOcean's image does: put the requested SSH
keys in root's `authorized_keys` and allow root login (`disable_root:
false`). Ubuntu cloud images refuse root by default; DigitalOcean's do not.

**D8. Base image: the official Ubuntu 24.04 cloud image, pinned.** The
stand-in for `ubuntu-24-04-x64` is `noble-server-cloudimg-amd64.img` from
`cloud-images.ubuntu.com`, pinned by SHA256 in the harness and cached
under `$XDG_CACHE_HOME/bivouac-e2e`, never in the repo. *Difference
accepted:* DigitalOcean's image adds its droplet agent and its own
cloud-init datasource; neither is used by bivouac.

**D9. Snapshots are disk copies.** A `power_off` action shuts the VM down
through QEMU's monitor; a `snapshot` action flattens the droplet's
overlay into a standalone qcow2 in the fake's store and creates the image
record: `type: snapshot`, `status: available` once the copy completes,
`min_disk_size` from the disk, the droplet's region. A later droplet
created from that image ID boots from a fresh overlay on it.

**D10. KVM through an ACL, not a group change.** This VM exposes `vmx`,
so nested KVM works, but the user is not in the `kvm` group and a group
change needs a new login. The harness setup grants the current user
`rw` on `/dev/kvm` with `setfacl`, which lasts until reboot. Without KVM
the harness refuses to run rather than crawl through TCG for hours.

**D11. Tier 2 is never in the default suite.** It is gated on
`BIVOUAC_E2E_QEMU=1`, lives in its own `e2e` package, and is not run by
`pre-commit` or `nix flake check`. It needs sudo, KVM, about 25 GB of
disk and an hour; none of that belongs in a unit run.

**D12. Tier 3 runs once, minimal and cleaned up.** After tiers 1 and 2
pass: one `bivouac image build` in the config's region with its size, one
`bivouac up` from `tag:<tag>`, then `down`, and delete the snapshot and
any temporary keys. The token is loaded from `~/sessions/.env` only into
the environment of the commands that need it, never printed or written
into the repo. Authorised by the user on 2026-10-17 ("go ahead and do the
test with digitalocean"), with the cost disclosed beforehand (under $1).

## Not covered by any tier

- DigitalOcean's billing and quota behaviour.
- Multiple regions: tier 2 has one region, the config's.
- Concurrent builds by two machines against one account.
