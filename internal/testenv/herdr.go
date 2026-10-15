package testenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// HerdrFixture is one real Herdr server in an isolated configuration and
// socket namespace. Its commands never inherit the hosting pane's HERDR_*
// routing, even when the test itself is running inside Herdr.
type HerdrFixture struct {
	Home       string
	ConfigRoot string
	Binary     string

	t       *testing.T
	root    string
	env     []string
	socket  string
	logPath string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan error
}

// StartHerdr starts the explicitly selected real Herdr binary as a headless
// server. Retirement integration is opt-in so ordinary tests cannot discover
// or stop a developer's running server by accident.
func StartHerdr(t *testing.T, binary string) *HerdrFixture {
	t.Helper()
	absolute := RequireHerdrBinary(t, binary)
	root, err := os.MkdirTemp("/tmp", "bivouac-herdr-")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	configRoot := filepath.Join(home, ".config", "herdr")
	for _, dir := range []string{
		configRoot,
		filepath.Join(root, "runtime"),
		filepath.Join(root, "state"),
		filepath.Join(root, "data"),
		filepath.Join(root, "cache"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_ = os.RemoveAll(root)
			t.Fatal(err)
		}
	}
	config := "[update]\nversion_check = false\nmanifest_check = false\n[terminal]\ndefault_shell = \"/bin/sh\"\n"
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte(config), 0o600); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}

	fixture := &HerdrFixture{
		Home:       home,
		ConfigRoot: configRoot,
		Binary:     absolute,
		t:          t,
		root:       root,
		socket:     filepath.Join(configRoot, "herdr.sock"),
		logPath:    filepath.Join(root, "herdr-fixture.log"),
	}
	fixture.env = isolatedHerdrEnv(root, home, configRoot)
	if err := fixture.start(); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.stop(); err != nil {
			t.Errorf("stop isolated Herdr: %v", err)
		}
		if t.Failed() {
			if log, err := os.ReadFile(fixture.logPath); err == nil && len(log) != 0 {
				t.Logf("isolated Herdr log:\n%s", log)
			}
		}
		_ = os.RemoveAll(root)
	})
	return fixture
}

// RequireHerdrBinary resolves and validates the supported opt-in executable.
// Keeping this check shared prevents the transport and ownership fixtures from
// silently proving behavior against different Herdr protocols.
func RequireHerdrBinary(t *testing.T, binary string) string {
	t.Helper()
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- the caller explicitly opts a test into this executable.
	version, err := exec.Command(absolute, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "herdr 0.9.4" {
		t.Fatalf("requires Herdr 0.9.4: %s: %v", version, err)
	}
	return absolute
}

// Run executes one client command against this fixture's socket.
func (f *HerdrFixture) Run(args ...string) (string, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// #nosec G204 -- Binary is the explicit test executable and args are test data.
	cmd := exec.CommandContext(ctx, f.Binary, args...)
	cmd.Env = f.env
	cmd.Dir = f.Home
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("herdr %s: %w", strings.Join(args, " "), ctx.Err())
	}
	return string(out), err
}

// Restart replaces the server while preserving its configuration namespace.
// Herdr workspaces are process state, so this deliberately permits public ids
// to be allocated again and lets ownership tests exercise stale handles.
func (f *HerdrFixture) Restart() error {
	if err := f.stop(); err != nil {
		return err
	}
	return f.start()
}

func (f *HerdrFixture) start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cmd != nil {
		return fmt.Errorf("fixture server is already running")
	}
	log, err := os.OpenFile(f.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// #nosec G204 -- Binary is the explicit opt-in integration executable.
	cmd := exec.Command(f.Binary, "server")
	cmd.Env = f.env
	cmd.Dir = f.Home
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return err
	}
	f.cmd = cmd
	f.done = make(chan error, 1)
	go func(done chan<- error) {
		err := cmd.Wait()
		_ = log.Close()
		done <- err
	}(f.done)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(f.socket); err == nil {
			return nil
		}
		select {
		case err := <-f.done:
			f.cmd = nil
			return fmt.Errorf("fixture server exited before creating its socket: %w", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	f.cmd = nil
	return fmt.Errorf("fixture server did not create %s", f.socket)
}

func (f *HerdrFixture) stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cmd == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- Binary is the explicit opt-in integration executable.
	stop := exec.CommandContext(ctx, f.Binary, "server", "stop")
	stop.Env = f.env
	stop.Dir = f.Home
	out, stopErr := stop.CombinedOutput()
	select {
	case waitErr := <-f.done:
		f.cmd = nil
		if stopErr != nil {
			return fmt.Errorf("server stop: %w: %s", stopErr, out)
		}
		if waitErr != nil {
			return fmt.Errorf("server exit: %w", waitErr)
		}
		return nil
	case <-ctx.Done():
		_ = f.cmd.Process.Kill()
		f.cmd = nil
		return fmt.Errorf("waiting for fixture server shutdown: %w", ctx.Err())
	}
}

func isolatedHerdrEnv(root, home, configRoot string) []string {
	env := make([]string, 0, len(os.Environ())+10)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if name == "HOME" || name == "SHELL" || name == "ZDOTDIR" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "HERDR_") {
			continue
		}
		env = append(env, variable)
	}
	return append(env,
		"HOME="+home,
		"SHELL=/bin/sh",
		"ZDOTDIR="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"HERDR_CONFIG_PATH="+filepath.Join(configRoot, "config.toml"),
		"HERDR_SOCKET_PATH="+filepath.Join(configRoot, "herdr.sock"),
	)
}
