package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/testenv"
)

// Opt in with BIVOUAC_HERDR_BIN=/absolute/path/to/herdr-0.9.4. No cloud or
// existing agent keys are used. Both the client and remote daemon are fixtures.
func TestHerdrPersistentSelectedTransport(t *testing.T) {
	binary := os.Getenv("BIVOUAC_HERDR_BIN")
	if binary == "" {
		t.Skip("set BIVOUAC_HERDR_BIN to Herdr 0.9.4 for actual SSH integration")
	}
	binary = testenv.RequireHerdrBinary(t, binary)
	ctx, c := transportFixture(t)
	unrelatedTransportAgent(t)
	// Short isolated paths also keep Herdr's control sockets below Unix limits.
	root, err := os.MkdirTemp("/tmp", "bivouac-herdr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "client")
	remoteHome := filepath.Join(root, "remote")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{home, remoteHome, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// API routing also follows HERDR_SOCKET_PATH, independently of HOME.
	// Never let fixture operations (especially cleanup) reach the hosting UI.
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "HERDR_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(home, ".config", "herdr", "config.toml"))
	if err := os.Symlink(binary, filepath.Join(bin, "herdr")); err != nil {
		t.Fatal(err)
	}
	for _, fixtureHome := range []string{home, remoteHome} {
		dir := filepath.Join(fixtureHome, ".config", "herdr")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		configuration := "[update]\nversion_check = false\nmanifest_check = false\n[terminal]\ndefault_shell = \"/bin/sh\"\n"
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(configuration), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(root, "ssh-argv")
	configLog := filepath.Join(root, "herdr-ssh-config")
	// Record actual Herdr argv/config. These fixture-only options select its
	// known_hosts and disable lingering multiplexers; they supply no identity.
	script := "#!/bin/sh\nif [ \"$1\" = -G ]; then exec " + shellQuote(realSSH) + " \"$@\"; fi\nprintf '%s\\n' \"$@\" >> " + shellQuote(argvLog) + "\nprevious=''\nfor argument in \"$@\"; do\n if [ \"$previous\" = -F ]; then cp \"$argument\" " + shellQuote(configLog) + "; fi\n previous=\"$argument\"\ndone\nexec " + shellQuote(realSSH) + " -o " + shellQuote("UserKnownHostsFile="+filepath.Join(home, ".ssh", "known_hosts")) + " -o StrictHostKeyChecking=yes -o ControlMaster=no -o ControlPersist=no \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	remoteEnv := append(os.Environ(), "HOME="+remoteHome, "XDG_CONFIG_HOME="+filepath.Join(remoteHome, ".config"), "HERDR_CONFIG_PATH="+filepath.Join(remoteHome, ".config", "herdr", "config.toml"), "SSH_AUTH_SOCK=", "SHELL=/bin/sh", "ZDOTDIR="+remoteHome, "XDG_STATE_HOME="+filepath.Join(remoteHome, ".local", "state"), "XDG_DATA_HOME="+filepath.Join(remoteHome, ".local", "share"), "XDG_CACHE_HOME="+filepath.Join(remoteHome, ".cache"), "HERDR_SOCKET_PATH="+filepath.Join(remoteHome, ".config", "herdr", "herdr.sock"))
	serverCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: c.Keys[0].PublicPath, Path: c.Keys[0].PublicPath, ConfigPath: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	c.PublicIP = streamSSHServer(t, key, func(command string) *exec.Cmd {
		// #nosec G204 -- disposable server executes only this test's real Herdr bootstrap commands.
		cmd := exec.CommandContext(serverCtx, "/bin/sh", "-c", command)
		cmd.Env = remoteEnv
		cmd.Dir = remoteHome
		return cmd
	})
	c.PreferredHost = c.PublicIP
	ctx = sshidentity.WithConnection(ctx, c)
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		// #nosec G204 -- stops the explicitly opted-in fixture daemon in isolated config.
		cmd := exec.CommandContext(stopCtx, binary, "server", "stop")
		cmd.Env = remoteEnv
		cmd.Dir = remoteHome
		_, _ = cmd.CombinedOutput()
	})
	client, err := reconcile.Connect(ctx, c.PublicIP, c.User)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	target, err := MachineTarget(ctx, c.PublicIP, c.User)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".log") || path == argvLog {
				raw, err := os.ReadFile(path)
				if err == nil && len(raw) < 50000 {
					t.Logf("fixture %s:\n%s", path, raw)
				}
			}
			return nil
		})
	})
	createCtx, finish := context.WithTimeout(ctx, 45*time.Second)
	id, label, err := EnsureMachine(localHerdr{ctx: createCtx}, c.Name, target, "")
	finish()
	if err != nil {
		t.Fatalf("actual Herdr machine add: %v", err)
	}
	if id == "" || label != c.Name {
		t.Fatalf("saved machine=%q/%q", id, label)
	}
	// The creating Herdr process has exited. A new process must reconnect using
	// the saved target and persistent include, with no bivouac identity context.
	reconnectCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	out, err := (localHerdr{ctx: reconnectCtx}).Run("--machine", id, "workspace", "list")
	if err != nil {
		t.Fatalf("saved-machine reconnection: %v: %s", err, out)
	}
	if !strings.Contains(out, "[") {
		t.Fatalf("workspace API did not return JSON: %s", out)
	}
	args, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), target) {
		t.Fatalf("actual SSH did not use alias target: %s", args)
	}
	managed, err := os.ReadFile(configLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(managed), filepath.Join(profiles.SSHDir, "config")) {
		t.Fatalf("Herdr did not include persistent user config: %s", managed)
	}
	// Herdr removes its temporary config on exit; the captured include chain
	// still resolves the same identity with a completely separate SSH process.
	// #nosec G204 -- actual SSH parses the disposable captured config only.
	effective, err := exec.Command(realSSH, "-G", "-F", configLog, c.Alias).CombinedOutput()
	if err != nil {
		t.Fatalf("captured config: %v: %s", err, effective)
	}
	if !strings.Contains(string(effective), "identitiesonly yes") || !strings.Contains(string(effective), c.Keys[0].PrivatePath) {
		t.Fatalf("Herdr effective identity changed: %s", effective)
	}
	t.Logf("Herdr 0.9.4 saved %s and a subsequent process reconnected through %s", id, target)
}
