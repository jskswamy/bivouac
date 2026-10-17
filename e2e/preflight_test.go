package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireHost skips unless the run was asked for, and fails with the
// missing piece when it was asked for but cannot work: a silent skip of
// a run someone explicitly requested would read as a pass.
func requireHost(t *testing.T) *host {
	t.Helper()
	if os.Getenv("BIVOUAC_E2E_QEMU") != "1" {
		t.Skip("set BIVOUAC_E2E_QEMU=1 to run the QEMU end-to-end test (needs sudo, KVM, ~25 GB and up to an hour)")
	}
	tools := map[string]string{}
	for _, tool := range []string{"qemu-system-x86_64", "qemu-img", "genisoimage", "packer", "ssh", "ssh-keygen", "git", "pkl", "sudo", "go"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s is not on PATH; run inside nix develop with: NIXPKGS_ALLOW_UNFREE=1 nix shell --impure nixpkgs#qemu_kvm nixpkgs#cdrkit nixpkgs#packer", tool)
		}
		tools[tool] = path
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Fatal("passwordless sudo is needed for the bridge and tap devices")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		t.Fatalf("/dev/kvm is not usable (%v); join the kvm group (sudo usermod -aG kvm $USER) and run the test under sg kvm", err)
	} else {
		_ = f.Close()
	}
	ctx := context.Background()
	if err := setupBridge(ctx, tools["sudo"]); err != nil {
		t.Fatal(err)
	}
	cache, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	h := &host{
		t: t, dir: t.TempDir(), cache: cache,
		qemu: tools["qemu-system-x86_64"], qemuImg: tools["qemu-img"], mkiso: tools["genisoimage"], sudo: tools["sudo"],
		memoryMB: 2048,
		vms:      map[int]*vm{}, disks: map[int]string{},
	}
	return h
}

// setupBridge creates the bridge and its NAT once per host; every step
// checks before it adds, so a second run, or a run after an aborted one,
// changes nothing. It is left in place afterwards: it is inert without
// taps, and removing it would race a concurrent run.
func setupBridge(ctx context.Context, sudo string) error {
	if exec.Command("ip", "link", "show", bridge).Run() != nil {
		if err := sh(ctx, sudo, "ip", "link", "add", bridge, "type", "bridge"); err != nil {
			return err
		}
		if err := sh(ctx, sudo, "ip", "addr", "add", gatewayIP+"/24", "dev", bridge); err != nil {
			return err
		}
	}
	if err := sh(ctx, sudo, "ip", "link", "set", bridge, "up"); err != nil {
		return err
	}
	if err := sh(ctx, sudo, "sysctl", "-q", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	rules := [][]string{
		{"-t", "nat", "POSTROUTING", "-s", subnet + ".0/24", "!", "-o", bridge, "-j", "MASQUERADE"},
		{"-t", "filter", "FORWARD", "-i", bridge, "-j", "ACCEPT"},
		{"-t", "filter", "FORWARD", "-o", bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}
	for _, r := range rules {
		table, chain, spec := r[1], r[2], r[3:]
		check := append([]string{"iptables", "-t", table, "-C", chain}, spec...)
		if exec.CommandContext(ctx, sudo, check...).Run() == nil {
			continue
		}
		// Inserted first in FORWARD, ahead of Docker's and Tailscale's
		// chains, whose default policy drops forwarded traffic.
		add := append([]string{"iptables", "-t", table, "-I", chain, "1"}, spec...)
		if err := sh(ctx, sudo, add...); err != nil {
			return err
		}
	}
	return nil
}

// cacheDir holds the base image, Packer's plugins and the last run's
// logs between runs. It is resolved before the test isolates $HOME, so a
// run does not download 600 MB into a temporary home every time, and it
// ignores XDG_CACHE_HOME because the dev shell points that into the
// checkout, where gigabytes of disk images do not belong.
func cacheDir() (string, error) {
	dir := os.Getenv("BIVOUAC_E2E_CACHE")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".cache", "bivouac-e2e")
	}
	return dir, os.MkdirAll(dir, 0o755)
}

// baseImagePath returns the pinned image, downloading and verifying it on
// first use. A checksum mismatch is fatal and the file is removed, so a
// truncated download is never booted.
func baseImagePath(t *testing.T, cache string) string {
	t.Helper()
	path := filepath.Join(cache, baseImageName)
	if sum, err := fileSHA256(path); err == nil && sum == baseImageSHA256 {
		return path
	}
	t.Logf("downloading %s", baseImageURL)
	resp, err := http.Get(baseImageURL) //nolint:gosec // a pinned constant URL, verified by checksum below
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downloading %s: %s", baseImageURL, resp.Status)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if sum, err := fileSHA256(tmp); err != nil || sum != baseImageSHA256 {
		_ = os.Remove(tmp)
		t.Fatalf("%s: checksum %s, want %s (%v)", baseImageURL, sum, baseImageSHA256, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readTail(path string, lines int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	all := strings.Split(string(b), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}

// fail formats a message with a console tail for each VM still alive,
// which is usually where the reason a build or boot stalled is.
func (b *qemuBackend) diagnostics() string {
	var out strings.Builder
	b.h.mu.Lock()
	ids := make([]int, 0, len(b.h.vms))
	for id := range b.h.vms {
		ids = append(ids, id)
	}
	b.h.mu.Unlock()
	for _, id := range ids {
		fmt.Fprintf(&out, "\n--- droplet %d console ---\n%s\n", id, b.consoleTail(id, 40))
	}
	return out.String()
}
