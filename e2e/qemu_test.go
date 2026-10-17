package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The bridge every VM's tap joins. Not 0.0.0.0:22-style port forwarding:
// Packer and bivouac both dial a droplet's address on port 22, and the
// host's own sshd already owns that port on every address.
const (
	bridge    = "bvce2e0"
	subnet    = "10.213.77"
	gatewayIP = subnet + ".1"
)

// baseImage is the stand-in for DigitalOcean's ubuntu-24-04-x64, pinned so
// a run is reproducible. Fetched once into the cache, never into the repo.
const (
	baseImageURL    = "https://cloud-images.ubuntu.com/releases/noble/release-20260926/ubuntu-24.04-server-cloudimg-amd64.img"
	baseImageSHA256 = "6a81c37564db9b1ee84e141922625e1d7c5b389b99bb3c572e0243607d5bb4d2"
	baseImageName   = "ubuntu-24.04-server-cloudimg-amd64.img"
)

// host holds the tools and state shared by every VM in one run.
type host struct {
	t        testing.TB
	dir      string // per-run scratch: overlays, seeds, sockets
	cache    string // pinned base image
	qemu     string // qemu-system-x86_64
	qemuImg  string
	mkiso    string // genisoimage
	sudo     string
	memoryMB int

	mu     sync.Mutex
	nextIP int
	vms    map[int]*vm
	disks  map[int]string // image ID -> standalone qcow2
}

type vm struct {
	id      int
	ip      string
	tap     string
	disk    string
	monitor string
	pidFile string
	console string
}

// sh runs a command, failing with its combined output so a harness error
// says what the tool said, not just an exit status.
func sh(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return nil
}

func (h *host) allocIP() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextIP++
	return subnet + "." + strconv.Itoa(9+h.nextIP)
}

// writeSeed writes a NoCloud seed doing for one droplet what
// DigitalOcean's metadata service and image do: the droplet's user data
// verbatim, its name as hostname, the requested keys for root, and root
// login allowed. A stock Ubuntu cloud image would otherwise create an
// "ubuntu" user and refuse root, which no DigitalOcean droplet does.
func (h *host) writeSeed(ctx context.Context, v *vm, name, userData string, keys []string, mac string) (string, error) {
	dir := filepath.Join(h.dir, fmt.Sprintf("seed-%d", v.id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var meta strings.Builder
	fmt.Fprintf(&meta, "instance-id: droplet-%d\nlocal-hostname: %s\npublic-keys:\n", v.id, name)
	for _, k := range keys {
		fmt.Fprintf(&meta, "  - %q\n", strings.TrimSpace(k))
	}
	files := map[string]string{
		"meta-data": meta.String(),
		"network-config": fmt.Sprintf(`version: 2
ethernets:
  nic0:
    match: {macaddress: "%s"}
    addresses: [%s/24]
    routes: [{to: default, via: %s}]
    nameservers: {addresses: [1.1.1.1, 8.8.8.8]}
`, mac, v.ip, gatewayIP),
		"vendor-data": "#cloud-config\ndisable_root: false\nssh_pwauth: false\nusers: []\n",
		"user-data":   userData,
	}
	if userData == "" {
		files["user-data"] = "#cloud-config\n{}\n"
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	iso := dir + ".iso"
	return iso, sh(ctx, h.mkiso, "-quiet", "-output", iso, "-volid", "cidata", "-joliet", "-rock", dir)
}

// boot starts a VM from backing (the base image or a snapshot) on a fresh
// overlay, so neither is ever written to.
func (h *host) boot(ctx context.Context, id int, name, userData string, keys []string, backing string, diskGB int) (*vm, error) {
	v := &vm{id: id, ip: h.allocIP(), tap: fmt.Sprintf("bvce2e-t%d", id%100000)}
	v.disk = filepath.Join(h.dir, fmt.Sprintf("droplet-%d.qcow2", id))
	v.monitor = filepath.Join(h.dir, fmt.Sprintf("droplet-%d.mon", id))
	v.pidFile = filepath.Join(h.dir, fmt.Sprintf("droplet-%d.pid", id))
	v.console = filepath.Join(h.dir, fmt.Sprintf("droplet-%d.console", id))
	last := v.ip[strings.LastIndex(v.ip, ".")+1:]
	n, _ := strconv.Atoi(last)
	mac := fmt.Sprintf("52:54:00:4d:00:%02x", n)

	if err := sh(ctx, h.qemuImg, "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", backing, v.disk, fmt.Sprintf("%dG", diskGB)); err != nil {
		return nil, err
	}
	iso, err := h.writeSeed(ctx, v, name, userData, keys, mac)
	if err != nil {
		return nil, err
	}
	if err := sh(ctx, h.sudo, "ip", "tuntap", "add", "dev", v.tap, "mode", "tap", "user", strconv.Itoa(os.Getuid())); err != nil {
		return nil, err
	}
	if err := sh(ctx, h.sudo, "ip", "link", "set", v.tap, "master", bridge, "up"); err != nil {
		return nil, err
	}
	if err := sh(ctx, h.qemu, "-enable-kvm", "-cpu", "host", "-m", strconv.Itoa(h.memoryMB), "-smp", "2",
		"-display", "none", "-serial", "file:"+v.console,
		"-monitor", "unix:"+v.monitor+",server,nowait",
		"-drive", "file="+v.disk+",if=virtio",
		"-drive", "file="+iso+",if=virtio,media=cdrom",
		"-netdev", "tap,id=n0,ifname="+v.tap+",script=no,downscript=no",
		"-device", "virtio-net-pci,netdev=n0,mac="+mac,
		"-daemonize", "-pidfile", v.pidFile); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.vms[id] = v
	h.mu.Unlock()
	return v, nil
}

// monitorCmd sends one human-monitor command.
func monitorCmd(sock, cmd string) error {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_, err = fmt.Fprintf(c, "%s\n", cmd)
	time.Sleep(200 * time.Millisecond)
	return err
}

func pidAlive(pidFile string) bool {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return false
	}
	_, err = os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// powerOff asks the guest to shut down through ACPI, as DigitalOcean's
// power_off does not but its shutdown does; Packer issues shutdown and
// falls back to power_off, so a clean guest shutdown is the realistic
// case and the hard stop is the fallback.
func (h *host) powerOff(id int, hard bool) error {
	v := h.get(id)
	if v == nil {
		return fmt.Errorf("droplet %d has no VM", id)
	}
	if !pidAlive(v.pidFile) {
		return nil
	}
	cmd := "system_powerdown"
	if hard {
		cmd = "quit"
	}
	if err := monitorCmd(v.monitor, cmd); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if !pidAlive(v.pidFile) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return monitorCmd(v.monitor, "quit")
}

// snapshot flattens a stopped droplet's overlay into a standalone qcow2,
// which later droplets boot from through their own overlays.
func (h *host) snapshot(ctx context.Context, id, imageID int) (int, error) {
	v := h.get(id)
	if v == nil {
		return 0, fmt.Errorf("droplet %d has no VM", id)
	}
	if pidAlive(v.pidFile) {
		return 0, errors.New("snapshot of a running droplet: power it off first")
	}
	out := filepath.Join(h.dir, fmt.Sprintf("image-%d.qcow2", imageID))
	if err := sh(ctx, h.qemuImg, "convert", "-O", "qcow2", v.disk, out); err != nil {
		return 0, err
	}
	h.mu.Lock()
	h.disks[imageID] = out
	h.mu.Unlock()
	// 0 lets the fake record the droplet's own disk size, as DigitalOcean
	// does for a snapshot's min_disk_size.
	return 0, nil
}

func (h *host) destroy(id int) error {
	v := h.get(id)
	if v == nil {
		return nil
	}
	_ = h.powerOff(id, true)
	_ = sh(context.Background(), h.sudo, "ip", "link", "del", v.tap)
	_ = os.Remove(v.disk)
	h.mu.Lock()
	delete(h.vms, id)
	h.mu.Unlock()
	return nil
}

func (h *host) get(id int) *vm {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.vms[id]
}
