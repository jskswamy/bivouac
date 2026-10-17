package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/jskswamy/bivouac/internal/provider/digitalocean/dofake"
	"github.com/jskswamy/bivouac/internal/testenv"
)

const (
	region = "nyc3"
	size   = "s-2vcpu-4gb"
	tag    = "e2e"
	name   = "e2e"
)

// TestImageBuildAndLaunch builds a custom image and launches from it with
// nothing faked but DigitalOcean itself: the real bivouac binary, real
// Packer with the real DigitalOcean plugin, and the real builder scripts
// running on a real Ubuntu, against dofake with QEMU VMs as droplets.
//
// Run it with:
//
//	sudo usermod -aG kvm $USER   # once; sg picks it up without a new login
//	sg kvm -c 'NIXPKGS_ALLOW_UNFREE=1 nix shell --impure nixpkgs#qemu_kvm nixpkgs#cdrkit nixpkgs#packer \
//	  -c nix develop -c env BIVOUAC_E2E_QEMU=1 go test -v -timeout 3h ./e2e/'
//
// Group membership rather than an ACL on /dev/kvm: udev re-applies the
// device's permissions and dropped a setfacl grant the first time a VM
// started.
//
// Logs of every bivouac run and each VM's console are kept in
// ~/.cache/bivouac-e2e/last-run (or $BIVOUAC_E2E_CACHE/last-run), which is
// replaced on each run.
func TestImageBuildAndLaunch(t *testing.T) {
	h := requireHost(t)
	base := baseImagePath(t, h.cache)
	logs := filepath.Join(h.cache, "last-run")
	_ = os.RemoveAll(logs)
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	h.dir = logs
	// The disks are kept after a failure, beside the logs, to inspect;
	// after a pass they are only gigabytes of nothing.
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		disks, _ := filepath.Glob(filepath.Join(logs, "*.qcow2"))
		for _, d := range disks {
			_ = os.Remove(d)
		}
	})
	plugins := filepath.Join(h.cache, "packer-plugins")
	bin := buildBivouac(t)

	// Isolated after the cache paths are resolved: bivouac's config,
	// state and ~/.ssh must not touch the real home, but a 600 MB image
	// and Packer's plugins should not be fetched again on every run.
	env := testenv.Isolate(t)

	fake := dofake.New(t)
	backend := &qemuBackend{h: h, base: base}
	fake.SetBackend(backend)
	t.Cleanup(backend.destroyAll)
	fake.AddSize(godo.Size{Slug: size, Memory: 4096, Vcpus: 2, Disk: 80, Regions: []string{region}, Available: true, PriceMonthly: 24, PriceHourly: 0.03571})
	fake.AddImage(godo.Image{Slug: stockSlug, Name: "Ubuntu 24.04 (LTS) x64", Type: "base", Distribution: "Ubuntu", Public: true, Regions: []string{region}, MinDiskSize: 7, Status: "available"})

	key := filepath.Join(env.Home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, "", nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "bivouac-e2e", "-f", key)
	// BIVOUAC_E2E_TEMPLATE swaps the served checkout for a published
	// reference, to rehearse offline exactly what a paid run will build.
	templates := os.Getenv("BIVOUAC_E2E_TEMPLATE")
	if templates == "" {
		templates = serveTemplates(t, t.TempDir())
	}
	repo := seedRepo(t, env.Home, key+".pub", templates)

	cli := func(stdin string, args ...string) string {
		t.Helper()
		e := append(os.Environ(),
			"DIGITALOCEAN_API_URL="+fake.URL(),
			"DIGITALOCEAN_TOKEN=bivouac-e2e-token",
			"PACKER_PLUGIN_PATH="+plugins,
			"CHECKPOINT_DISABLE=1",
			// The keys under test are the files in the isolated home, not
			// whatever the developer's agent holds.
			"SSH_AUTH_SOCK=",
		)
		return runLogged(t, logs, repo, e, stdin, bin, args...)
	}

	// --- Build ---------------------------------------------------------
	start := time.Now()
	out := cli("", "image", "build", "--tag", tag, "--keep-previous", "0", "--json")
	t.Logf("image build took %s", time.Since(start).Round(time.Second))
	var built struct {
		ImageID   string `json:"image_id"`
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal([]byte(lastJSON(out)), &built); err != nil {
		t.Fatalf("image build --json: %v\n%s", err, out)
	}
	imageID, err := strconv.Atoi(built.ImageID)
	if err != nil {
		t.Fatalf("image ID %q: %v", built.ImageID, err)
	}
	snap := findImage(t, fake, imageID)
	for _, want := range []string{"bivouac-image", "bivouac-format-1", "bivouac-arch-x86_64", "bivouac-region-" + region, tag} {
		if !slices.Contains(snap.Tags, want) {
			t.Errorf("snapshot tags %v lack %q", snap.Tags, want)
		}
	}
	if snap.Type != "snapshot" || snap.Status != "available" {
		t.Errorf("snapshot = %+v, want an available snapshot", snap)
	}
	if left := fake.Droplets(); len(left) != 0 {
		t.Errorf("Packer left droplets behind: %+v", left)
	}
	if cfg := readFile(t, filepath.Join(repo, "bivouac.pkl")); !strings.Contains(cfg, `image = "tag:`+tag+`"`) {
		t.Errorf("bivouac.pkl was not pointed at the image:\n%s", cfg)
	}

	// --- Launch from the image -----------------------------------------
	start = time.Now()
	cli("y\n", "up", name)
	t.Logf("up from the image took %s", time.Since(start).Round(time.Second))
	droplets := fake.Droplets()
	if len(droplets) != 1 || droplets[0].Image == nil || droplets[0].Image.ID != imageID {
		t.Fatalf("droplets = %+v, want one created from image %d%s", droplets, imageID, backend.diagnostics())
	}
	ip := droplets[0].Networks.V4[0].IPAddress
	user := currentUser(t)
	remote := func(script string) string {
		t.Helper()
		return run(t, "", nil, "ssh", "-i", key, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", user+"@"+ip, script)
	}

	// The baked boot path installs nothing: Nix and the closure came
	// with the image, and cloud-init only set up the account.
	if log := remote("sudo cat /var/log/cloud-init-output.log"); strings.Contains(log, "nix-installer") {
		t.Errorf("the launch ran the Nix installer; a baked image should skip it:\n%s", log)
	}
	rooted := strings.TrimSpace(remote("readlink /nix/var/nix/gcroots/bivouac-image/closure"))
	if !strings.HasPrefix(rooted, "/nix/store/") {
		t.Errorf("baked closure GC root = %q, want a store path", rooted)
	}
	active := strings.TrimSpace(remote(`readlink -f "$HOME/.local/state/nix/profiles/home-manager"`))
	t.Logf("baked closure %s; active home-manager generation %s", rooted, active)
	if active != rooted {
		t.Errorf("the launch activated %s, not the baked closure %s: reconcile rebuilt the environment the image was meant to carry", active, rooted)
	}
	if shell := strings.TrimSpace(remote("getent passwd \"$USER\" | cut -d: -f7")); shell == "" {
		t.Error("the launch user has no login shell")
	}

	// --- Tear down -----------------------------------------------------
	cli("y\n", "down", name)
	if left := fake.Droplets(); len(left) != 0 {
		t.Errorf("down left droplets: %+v", left)
	}
}

// buildBivouac compiles this checkout's binary, so the test exercises
// the code under review and not whatever bivouac is installed.
func buildBivouac(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bivouac")
	run(t, "..", nil, "go", "build", "-o", bin, ".")
	return bin
}

// seedRepo makes the project repository a launch provisions: one commit
// and a bivouac.pkl naming the test's key and the served templates.
func seedRepo(t *testing.T, home, pub, template string) string {
	t.Helper()
	repo := filepath.Join(home, "work", "e2e-project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("region = %q\nsize = %q\ntemplate = %q\ntailscale = false\nsshKeys { %q }\n", region, size, template, pub)
	writeFile(t, filepath.Join(repo, "bivouac.pkl"), cfg)
	writeFile(t, filepath.Join(repo, "README.md"), "bivouac e2e fixture\n")
	git := func(args ...string) { run(t, repo, nil, "git", args...) }
	git("init", "-q", "-b", "main")
	git("-c", "user.name=bivouac e2e", "-c", "user.email=e2e@example.invalid", "add", ".")
	git("-c", "user.name=bivouac e2e", "-c", "user.email=e2e@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Seed the e2e fixture")
	return repo
}

func findImage(t *testing.T, fake *dofake.Server, id int) godo.Image {
	t.Helper()
	for _, img := range fake.Images() {
		if img.ID == id {
			return img
		}
	}
	t.Fatalf("image %d is not in the fake: %+v", id, fake.Images())
	return godo.Image{}
}

// lastJSON is the last line of out that starts a JSON object: progress
// goes to stderr, but a stray line on stdout should not hide the result.
func lastJSON(out string) string {
	i := strings.LastIndex(out, "\n{")
	if i < 0 {
		return strings.TrimSpace(out)
	}
	return strings.TrimSpace(out[i:])
}

func currentUser(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(run(t, "", nil, "id", "-un"))
}

func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s%s", name, strings.Join(args, " "), err, out.String(), errOut.String())
	}
	return out.String()
}

// runLogged runs a bivouac command with its output teed into a log file
// that outlives the test, since a build's real failure is usually a page
// above where it stopped.
func runLogged(t *testing.T, logs, dir string, env []string, stdin, bin string, args ...string) string {
	t.Helper()
	logPath := filepath.Join(logs, "bivouac-"+args[0]+"-"+strings.Join(args[1:min(len(args), 2)], "")+".log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out, f)
	cmd.Stderr = f
	if err := cmd.Run(); err != nil {
		tail, _ := readTail(logPath, 60)
		t.Fatalf("bivouac %s: %v (log %s)\n%s", strings.Join(args, " "), err, logPath, tail)
	}
	return out.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
