package image

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The builder scripts run on a temporary Droplet as root, so these tests
// drive them through the knobs they expose for exactly this purpose: a
// root prefix for every path they touch, and PATH for every command they
// do not reach by absolute path. What only a real builder can show --
// cloud-init's own behaviour, a real Nix build, the snapshot's
// cleanliness -- is left to the paid smoke test.

func scriptPath(name string) string { return filepath.Join("bundle", "scripts", name) }

// runScript runs a builder script under bash with extra environment and
// returns its combined output.
func runScript(t *testing.T, name string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{scriptPath(name)}, args...)...) // #nosec G204 -- fixed test scripts
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// stub writes an executable shell script at path.
func stub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil { // #nosec G306 -- a test stub must be executable
		t.Fatal(err)
	}
}

// loggingStubs puts a stub for each named command into a fresh directory,
// each appending its name and arguments to log, and returns a PATH that
// finds them first.
func loggingStubs(t *testing.T, log string, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		stub(t, filepath.Join(dir, n), `echo "`+n+` $*" >> `+shellQuote(log))
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestBuilderScripts_Syntax(t *testing.T) {
	opts := bundleOpts(t, selected(withPackages))
	generate(t, opts)
	scripts, err := filepath.Glob(filepath.Join(opts.OutputDir, "scripts", "*.sh"))
	if err != nil || len(scripts) != 4 {
		t.Fatalf("scripts = %v, %v; want the bundle's four", scripts, err)
	}
	for _, s := range scripts {
		if out, err := exec.Command("bash", "-n", s).CombinedOutput(); err != nil { // #nosec G204 -- generated test bundle
			t.Errorf("bash -n %s: %v\n%s", filepath.Base(s), err, out)
		}
	}
	t.Run("shellcheck", func(t *testing.T) {
		if _, err := exec.LookPath("shellcheck"); err != nil {
			t.Skip("shellcheck not installed")
		}
		for _, s := range scripts {
			args := []string{s}
			// Alone, as the builder's user data, the bootstrap sets
			// LOGIN_SHELL for an account-creation step that only a
			// launch's cloud-init.sh appends.
			if filepath.Base(s) == "bootstrap.sh" {
				args = []string{"--exclude=SC2034", s}
			}
			if out, err := exec.Command("shellcheck", args...).CombinedOutput(); err != nil { // #nosec G204 -- generated test bundle
				t.Errorf("shellcheck %s: %v\n%s", filepath.Base(s), err, out)
			}
		}
	})
}

// The builder must not create the enrollments the spec forbids, nor ever
// see the provider credential.
func TestBuilderScripts_NoEnrollmentOrCredentials(t *testing.T) {
	for _, name := range builderScripts {
		b, err := os.ReadFile(scriptPath(name))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(b))
		for _, banned := range []string{"tailscale up", "moshi", "gh auth", "dolt login", "digitalocean_token"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s contains %q", name, banned)
			}
		}
	}
}

func TestSanitize_FakeRoot(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"home/alice/.config/x":                 "builder state",
		"root/.ssh/authorized_keys":            "ssh-ed25519 AAAA packer",
		"etc/ssh/ssh_host_ed25519_key":         "private",
		"etc/ssh/ssh_host_ed25519_key.pub":     "public",
		"etc/ssh/sshd_config":                  "keep me",
		"etc/machine-id":                       "0123456789abcdef0123456789abcdef\n",
		"var/lib/cloud/instances/x/user-data":  "#!/bin/bash",
		"root/.bash_history":                   "export SECRET=1",
		"var/log/syslog":                       "build log line",
		"var/log/syslog.1":                     "older build log",
		"tmp/x":                                "scratch",
		"var/tmp/y":                            "scratch",
		"nix/store/abc/bin/hello":              "store",
		"var/lib/bivouac-image/metadata.json":  "{}",
		"nix/var/nix/profiles/default/bin/nix": "nix",
	})
	gcroot := filepath.Join(root, "nix/var/nix/gcroots/bivouac-image/closure")
	if err := os.MkdirAll(filepath.Dir(gcroot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nix/store/abc"), gcroot); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls")
	path := loggingStubs(t, log, "userdel", "pkill", "cloud-init", "journalctl", "sync")

	out, err := runScript(t, "sanitize.sh", []string{"SANITIZE_ROOT=" + root, "PATH=" + path}, "alice")
	if err != nil {
		t.Fatalf("sanitize.sh: %v\n%s", err, out)
	}

	for _, gone := range []string{
		"home/alice", "etc/ssh/ssh_host_ed25519_key", "etc/ssh/ssh_host_ed25519_key.pub",
		"var/lib/cloud/instances/x", "root/.bash_history", "tmp/x", "var/tmp/y", "var/log/syslog.1",
	} {
		if exists(filepath.Join(root, gone)) {
			t.Errorf("%s survived sanitizing", gone)
		}
	}
	for _, emptied := range []string{"root/.ssh/authorized_keys", "etc/machine-id", "var/log/syslog"} {
		info, err := os.Stat(filepath.Join(root, emptied))
		if err != nil || info.Size() != 0 {
			t.Errorf("%s: %v, size %v; want it present and empty", emptied, err, info)
		}
	}
	for _, kept := range []string{
		"nix/store/abc/bin/hello", "nix/var/nix/gcroots/bivouac-image/closure",
		"var/lib/bivouac-image/metadata.json", "nix/var/nix/profiles/default/bin/nix", "etc/ssh/sshd_config",
		"tmp", "var/tmp",
	} {
		if !exists(filepath.Join(root, kept)) {
			t.Errorf("%s was removed; it must survive into the image", kept)
		}
	}

	// Under a fake root the commands that act on the real machine are
	// reported, not run.
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("commands ran under SANITIZE_ROOT:\n%s", b)
	}
	for _, want := range []string{"userdel -r alice", "cloud-init clean --logs --machine-id --seed", "journalctl --rotate --vacuum-time=1s"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not report %q:\n%s", want, out)
		}
	}
}

// A missing GC root means the closure is unprotected: the image would
// look fine and lose its store paths to the first garbage collection.
func TestSanitize_FailsWithoutGCRoot(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"root/.ssh/authorized_keys": "k", "etc/machine-id": "id"})
	path := loggingStubs(t, filepath.Join(t.TempDir(), "calls"), "userdel", "pkill", "cloud-init", "journalctl", "sync")
	if out, err := runScript(t, "sanitize.sh", []string{"SANITIZE_ROOT=" + root, "PATH=" + path}, "alice"); err == nil {
		t.Fatalf("sanitize.sh succeeded without a GC root:\n%s", out)
	}
}

func TestSanitize_RejectsInvalidUser(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	path := loggingStubs(t, log, "userdel", "pkill", "cloud-init", "journalctl", "sync")
	for _, user := range []string{"", "Bad User", "../root"} {
		if out, err := runScript(t, "sanitize.sh", []string{"SANITIZE_ROOT=" + root, "PATH=" + path}, user); err == nil {
			t.Errorf("sanitize.sh %q succeeded:\n%s", user, out)
		}
	}
}

func TestBuildClosure_RejectsInvalidUserBeforeRunningAnything(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	path := loggingStubs(t, log, "useradd", "runuser", "python3", "mkdir", "chmod")
	stub(t, filepath.Join(root, "nix/var/nix/profiles/default/bin/nix"), `echo "nix $*" >> `+shellQuote(log))
	for _, user := range []string{"Bad User", "", "root;id"} {
		out, err := runScript(t, "build-closure.sh", []string{"BUILD_ROOT=" + root, "PATH=" + path}, user)
		if err == nil {
			t.Errorf("build-closure.sh %q succeeded:\n%s", user, out)
		}
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("commands ran before the username was validated:\n%s", b)
	}
}

// buildClosureRoot lays out what the builder holds when build-closure.sh
// starts: the uploaded metadata (and flake, for a rendered config) and a
// Nix whose build prints a store path and whose metadata names a
// revision. useradd and runuser are stubbed; runuser runs the command it
// was given, so the Nix stub sees exactly what the build user's shell
// would have run.
func buildClosureRoot(t *testing.T, flakeRef string) (root, log, path string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	root = t.TempDir()
	log = filepath.Join(t.TempDir(), "calls")
	writeTree(t, root, map[string]string{
		"var/lib/bivouac-image/metadata.json": `{"format_version": 1, "flake_ref": "` + flakeRef + `", "username": "alice"}`,
		"var/lib/bivouac-image/nix/flake.nix": "{ }",
	})
	bin := filepath.Join(root, "nix/var/nix/profiles/default/bin")
	stub(t, filepath.Join(bin, "nix"), `printf 'nix' >> `+shellQuote(log)+`; printf ' [%s]' "$@" >> `+shellQuote(log)+`; echo >> `+shellQuote(log)+`
case "$1" in
  build) echo /nix/store/aaaa-home-manager-generation ;;
  flake) echo '{"url":"github:o/r/1234","revision":"1234","locks":{"nodes":{"root":{"inputs":{}},"nixpkgs":{"locked":{"rev":"5678","type":"github"}}}}}' ;;
esac`)
	stub(t, filepath.Join(bin, "nix-store"), `printf 'nix-store' >> `+shellQuote(log)+`; printf ' [%s]' "$@" >> `+shellQuote(log)+`; echo >> `+shellQuote(log))
	dir := t.TempDir()
	stub(t, filepath.Join(dir, "useradd"), `echo "useradd $*" >> `+shellQuote(log))
	// runuser -u USER -- CMD...: log the user, then run CMD.
	stub(t, filepath.Join(dir, "runuser"), `echo "runuser $1 $2" >> `+shellQuote(log)+`
while [ "$1" != "--" ]; do shift; done; shift
exec "$@"`)
	return root, log, dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestBuildClosure_BuildsAndRootsWithoutActivating(t *testing.T) {
	root, log, path := buildClosureRoot(t, "path:./nix#default")
	out, err := runScript(t, "build-closure.sh", []string{"BUILD_ROOT=" + root, "PATH=" + path}, "alice")
	if err != nil {
		t.Fatalf("build-closure.sh: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(log)
	calls := string(b)

	if !strings.Contains(calls, "useradd --create-home --shell /bin/bash alice") {
		t.Errorf("no builder account created as the launch user:\n%s", calls)
	}
	if !strings.Contains(calls, "runuser -u alice") {
		t.Errorf("the build did not run as the launch user:\n%s", calls)
	}
	// The bundle-relative flake is rewritten to where it was uploaded,
	// and the build takes reconcile's flags, minus the switch.
	installable := "[path:" + root + `/var/lib/bivouac-image/nix#homeConfigurations."default".activationPackage]`
	wantBuild := "nix [build] [--impure] [--no-write-lock-file] [--refresh] [--no-link] [--print-out-paths] " + installable
	if !strings.Contains(calls, wantBuild) {
		t.Errorf("build call missing; want %s in:\n%s", wantBuild, calls)
	}
	wantRoot := "nix-store [--add-root] [" + root + "/nix/var/nix/gcroots/bivouac-image/closure] [--realise] [/nix/store/aaaa-home-manager-generation]"
	if !strings.Contains(calls, wantRoot) {
		t.Errorf("GC root call missing; want %s in:\n%s", wantRoot, calls)
	}
	for _, banned := range []string{"switch", "/activate"} {
		if strings.Contains(calls, banned) {
			t.Errorf("the closure was activated or run (%q):\n%s", banned, calls)
		}
	}

	meta := readFile(t, filepath.Join(root, "var/lib/bivouac-image/metadata.json"))
	for _, want := range []string{`"revisions"`, `"1234"`, `"5678"`, `"flake_ref"`, `"username"`} {
		if !strings.Contains(meta, want) {
			t.Errorf("metadata lacks %s:\n%s", want, meta)
		}
	}
}

// A template ref with no fragment means what home-manager makes of it:
// the configuration named after the user.
func TestBuildClosure_RemoteRefWithoutFragment(t *testing.T) {
	root, log, path := buildClosureRoot(t, "github:me/dotfiles")
	if out, err := runScript(t, "build-closure.sh", []string{"BUILD_ROOT=" + root, "PATH=" + path}, "alice"); err != nil {
		t.Fatalf("build-closure.sh: %v\n%s", err, out)
	}
	calls := readFile(t, log)
	if want := `[github:me/dotfiles#homeConfigurations."alice".activationPackage]`; !strings.Contains(calls, want) {
		t.Errorf("want %s in:\n%s", want, calls)
	}
}

// waitRoot is a builder on which bootstrap finished: Nix is installed and
// the requested shells are registered.
func waitRoot(t *testing.T, shells ...string) string {
	t.Helper()
	root := t.TempDir()
	stub(t, filepath.Join(root, "nix/var/nix/profiles/default/bin/nix"), `echo "nix (Nix) 2.0"`)
	registered := "/bin/sh\n/bin/bash\n"
	stub(t, filepath.Join(root, "bin/bash"), "exit 0")
	for _, s := range shells {
		stub(t, filepath.Join(root, "usr/bin", s), "exit 0")
		registered += "/usr/bin/" + s + "\n"
	}
	writeTree(t, root, map[string]string{"etc/shells": registered})
	return root
}

func cloudInitPath(t *testing.T, status string, code int) string {
	t.Helper()
	dir := t.TempDir()
	stub(t, filepath.Join(dir, "cloud-init"), `echo "status: `+status+`"; exit `+string(rune('0'+code)))
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestWaitBootstrap(t *testing.T) {
	cases := []struct {
		name    string
		shell   string
		root    func(t *testing.T) string
		status  string
		code    int
		wantErr bool
	}{
		{"done with fish", "fish", func(t *testing.T) string { return waitRoot(t, "fish") }, "done", 0, false},
		{"done with bash", "bash", func(t *testing.T) string { return waitRoot(t) }, "done", 0, false},
		{"cloud-init error", "fish", func(t *testing.T) string { return waitRoot(t, "fish") }, "error", 1, true},
		{"error reported with a zero exit", "fish", func(t *testing.T) string { return waitRoot(t, "fish") }, "error", 0, true},
		// The bootstrap falls back to bash rather than failing; an image
		// tagged with fish must actually carry it.
		{"shell missing", "fish", func(t *testing.T) string { return waitRoot(t) }, "done", 0, true},
		{"shell not registered", "fish", func(t *testing.T) string {
			root := waitRoot(t)
			stub(t, filepath.Join(root, "usr/bin/fish"), "exit 0")
			return root
		}, "done", 0, true},
		{"nix missing", "fish", func(t *testing.T) string {
			root := waitRoot(t, "fish")
			if err := os.Remove(filepath.Join(root, "nix/var/nix/profiles/default/bin/nix")); err != nil {
				t.Fatal(err)
			}
			return root
		}, "done", 0, true},
		{"invalid shell name", "../fish", func(t *testing.T) string { return waitRoot(t, "fish") }, "done", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{"WAIT_ROOT=" + tc.root(t), "PATH=" + cloudInitPath(t, tc.status, tc.code)}
			out, err := runScript(t, "wait-bootstrap.sh", env, tc.shell)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v\n%s", err, tc.wantErr, out)
			}
		})
	}
}

// A bootstrap that never finishes must fail the build, not hang it.
func TestWaitBootstrap_TimesOut(t *testing.T) {
	dir := t.TempDir()
	stub(t, filepath.Join(dir, "cloud-init"), "exec sleep 30")
	env := []string{
		"WAIT_ROOT=" + waitRoot(t, "fish"),
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"BOOTSTRAP_TIMEOUT=1",
	}
	if out, err := runScript(t, "wait-bootstrap.sh", env, "fish"); err == nil {
		t.Fatalf("wait-bootstrap.sh succeeded although cloud-init never finished:\n%s", out)
	}
}
