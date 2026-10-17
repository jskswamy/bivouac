package e2e

import (
	"context"
	"fmt"
	"strings"

	"github.com/digitalocean/godo"

	"github.com/jskswamy/bivouac/internal/provider/digitalocean/dofake"
)

// stockSlug is the one stock image the fake boots, and the source every
// bivouac image build starts from.
const stockSlug = "ubuntu-24-04-x64"

// qemuBackend turns the fake API's droplets into QEMU VMs. A droplet
// created from the stock slug boots the pinned Ubuntu image; one created
// from an image ID boots the disk that ID's snapshot captured.
type qemuBackend struct {
	h    *host
	base string // path of the pinned base image
}

var _ dofake.Backend = (*qemuBackend)(nil)

func (b *qemuBackend) Create(d godo.Droplet, userData string, keys []godo.Key) (string, error) {
	backing, err := b.backing(d.Image)
	if err != nil {
		return "", err
	}
	pub := make([]string, 0, len(keys))
	for _, k := range keys {
		pub = append(pub, k.PublicKey)
	}
	disk := d.Disk
	if disk == 0 {
		disk = 25
	}
	b.h.t.Logf("droplet %d (%s): booting from %s", d.ID, d.Name, backing)
	v, err := b.h.boot(context.Background(), d.ID, d.Name, userData, pub, backing, disk)
	if err != nil {
		return "", err
	}
	return v.ip, nil
}

func (b *qemuBackend) backing(img *godo.Image) (string, error) {
	if img == nil {
		return "", fmt.Errorf("droplet has no image")
	}
	// The slug first: the fake gives a seeded stock image an ID too.
	if img.Slug == stockSlug {
		return b.base, nil
	}
	b.h.mu.Lock()
	defer b.h.mu.Unlock()
	if disk, ok := b.h.disks[img.ID]; ok {
		return disk, nil
	}
	return "", fmt.Errorf("the QEMU backend boots %s and captured snapshots, not image %d %q", stockSlug, img.ID, img.Slug)
}

// PowerOff is DigitalOcean's shutdown when the guest cooperates. Packer
// asks for shutdown and only then power_off, both of which land here.
func (b *qemuBackend) PowerOff(id int) error { return b.h.powerOff(id, false) }

func (b *qemuBackend) Snapshot(id, imageID int) (int, error) {
	b.h.t.Logf("droplet %d: capturing its disk as image %d", id, imageID)
	return b.h.snapshot(context.Background(), id, imageID)
}

func (b *qemuBackend) Destroy(id int) error {
	b.h.t.Logf("droplet %d: destroying", id)
	return b.h.destroy(id)
}

// destroyAll removes every VM still running, for the test's cleanup: a
// failure part way through must not leave VMs or tap devices behind.
func (b *qemuBackend) destroyAll() {
	b.h.mu.Lock()
	ids := make([]int, 0, len(b.h.vms))
	for id := range b.h.vms {
		ids = append(ids, id)
	}
	b.h.mu.Unlock()
	for _, id := range ids {
		_ = b.h.destroy(id)
	}
}

// consoleTail is the end of a VM's serial console, for a failure message.
func (b *qemuBackend) consoleTail(id int, lines int) string {
	v := b.h.get(id)
	if v == nil {
		return ""
	}
	out, err := readTail(v.console, lines)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(out)
}
