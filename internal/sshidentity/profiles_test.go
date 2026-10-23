package sshidentity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshkeys"
)

func profileFixture(t *testing.T, name string) (ProfileStore, Connection) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SSH_AUTH_SOCK", "")
	key, _ := fixture(t, true)
	ids, err := Select(context.Background(), []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	return ProfileStore{ConfigDir: filepath.Join(dir, "bivouac"), SSHDir: filepath.Join(dir, "ssh")}, Connection{Name: name, Alias: "bivouac-" + name, User: "me", PublicIP: "192.0.2.1", PreferredHost: "192.0.2.1", Keys: ids}
}

func TestProfilesRealOpenSSHAndSharedInclude(t *testing.T) {
	store, c := profileFixture(t, "one")
	if err := store.Check(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("preflight wrote persistent files")
	}
	if err := os.MkdirAll(store.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := "Host personal\n  HostName example.com\nHost *\n  ForwardAgent no\n"
	userConfig := filepath.Join(store.SSHDir, "config")
	write(t, userConfig, []byte(original))
	for i := 0; i < 2; i++ {
		if err := store.Upsert(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	c2 := c
	c2.Name = "two"
	c2.Alias = "bivouac-two"
	c2.PreferredHost = "192.0.2.2"
	if err := store.Upsert(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "# BEGIN bivouac managed SSH include") != 1 || !strings.HasSuffix(string(raw), original) {
		t.Fatalf("user content changed: %s", raw)
	}
	managed, err := os.ReadFile(filepath.Join(store.ConfigDir, "ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(managed), "Host bivouac-one") || !strings.Contains(string(managed), "Host bivouac-two") {
		t.Fatal(string(managed))
	}
	if err := store.Remove(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	managed, err = os.ReadFile(filepath.Join(store.ConfigDir, "ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(managed), "Host bivouac-one") || !strings.Contains(string(managed), "Host bivouac-two") {
		t.Fatal(string(managed))
	}
	if err := store.Check(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
}

func TestProfilesRejectUserIdentityConflictsAndSymlinks(t *testing.T) {
	store, c := profileFixture(t, "machine")
	if err := os.MkdirAll(store.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.SSHDir, "config")
	write(t, path, []byte("Host *\n  IdentityFile /unrelated/key\n"))
	if err := store.Check(context.Background(), c); err == nil || !strings.Contains(err.Error(), "IdentityFile") {
		t.Fatalf("conflict=%v", err)
	}
	if _, err := os.Stat(store.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("conflict wrote files")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "config")
	write(t, target, []byte("# user content\n"))
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	// A symlink to a writable file is followed: the include lands in the
	// target and the link itself survives.
	if err := store.Upsert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	raw, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(raw), includeBegin) || !strings.HasSuffix(string(raw), "# user content\n") {
		t.Fatalf("target not updated: %q %v", raw, err)
	}
}

func readOnlySymlinkedConfig(t *testing.T, store ProfileStore, content string) (link, target string) {
	t.Helper()
	if err := os.MkdirAll(store.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(t.TempDir(), "config")
	write(t, target, []byte(content))
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(store.SSHDir, "config")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

func TestReadOnlyConfigWithoutIncludeExplainsWhatToAdd(t *testing.T) {
	store, c := profileFixture(t, "machine")
	_, target := readOnlySymlinkedConfig(t, store, "# managed by home-manager\n")
	err := store.Upsert(context.Background(), c)
	managed, _ := filepath.Abs(filepath.Join(store.ConfigDir, "ssh", "config"))
	if err == nil || !strings.Contains(err.Error(), "Include \""+managed+"\"") {
		t.Fatalf("want guidance naming the include line, got %v", err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "# managed by home-manager\n" {
		t.Fatal("read-only target changed")
	}
	if _, err := os.Stat(store.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("failed preflight wrote files")
	}
}

func TestReadOnlyConfigWithExistingIncludeIsLeftAlone(t *testing.T) {
	managedPath := func(s ProfileStore) string {
		m, _ := filepath.Abs(filepath.Join(s.ConfigDir, "ssh", "config"))
		return m
	}
	// Home Manager joins programs.ssh.includes into one multi-path line.
	for name, line := range map[string]func(string) string{
		"single":     func(m string) string { return "Include " + m },
		"multi-path": func(m string) string { return "Include /elsewhere/config_external " + m },
	} {
		t.Run(name, func(t *testing.T) {
			readOnlyIncludeCase(t, managedPath, line)
		})
	}
}

func readOnlyIncludeCase(t *testing.T, managedPath func(ProfileStore) string, line func(string) string) {
	store, c := profileFixture(t, "machine")
	managed := managedPath(store)
	content := line(managed) + "\nHost personal\n  HostName example.com\n"
	_, target := readOnlySymlinkedConfig(t, store, content)
	for i := 0; i < 2; i++ {
		if err := store.Upsert(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if raw, _ := os.ReadFile(target); string(raw) != content {
		t.Fatalf("target changed: %q", raw)
	}
	raw, err := os.ReadFile(managed)
	if err != nil || !strings.Contains(string(raw), "Host "+c.Alias) {
		t.Fatalf("profile not published: %q %v", raw, err)
	}
}

func TestProfileInjectionAndAgentIdentity(t *testing.T) {
	store, c := profileFixture(t, "machine")
	for _, name := range []string{"bad\nHost *", "../bad", "bad_name"} {
		if _, err := Alias(name); err == nil {
			t.Fatalf("alias injection %q", name)
		}
	}
	for _, path := range []string{"/key\nIdentityFile /other", "/key\x00"} {
		bad := c
		bad.Keys = append([]Identity(nil), c.Keys...)
		bad.Keys[0].PrivatePath = path
		if err := store.Check(context.Background(), bad); err == nil {
			t.Fatalf("path injection %q", path)
		}
	}
	c.Keys[0].Agent = true
	c.Keys[0].PrivatePath = ""
	if err := store.Upsert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.ConfigDir, "ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), c.Keys[0].PublicPath) {
		t.Fatal("agent public path lost")
	}
}

func TestConcurrentProfilesPreserveMachines(t *testing.T) {
	store, c := profileFixture(t, "one")
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two", "three"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			next := c
			next.Name = name
			next.Alias = "bivouac-" + name
			payload, err := json.Marshal(struct {
				Store      ProfileStore
				Connection Connection
			}{store, next})
			if err != nil {
				t.Error(err)
				return
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestProfileSubprocess$")
			cmd.Env = append(os.Environ(), "BIVOUAC_PROFILE_TEST="+string(payload))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("subprocess: %v: %s", err, output)
			}
		}(name)
	}
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(store.ConfigDir, "ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if !strings.Contains(string(raw), "Host bivouac-"+name) {
			t.Fatal(string(raw))
		}
	}
}

func TestProfileSubprocess(t *testing.T) {
	payload := os.Getenv("BIVOUAC_PROFILE_TEST")
	if payload == "" {
		return
	}
	var input struct {
		Store      ProfileStore
		Connection Connection
	}
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		t.Fatal(err)
	}
	if err := input.Store.Upsert(context.Background(), input.Connection); err != nil {
		t.Fatal(err)
	}
}

func TestProfileRetryRestoresPublishedIndex(t *testing.T) {
	store, c := profileFixture(t, "machine")
	if err := store.Upsert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.managedPath()); err != nil {
		t.Fatal(err)
	}
	c.PreferredHost = "192.0.2.3"
	if err := store.Upsert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.managedPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "HostName 192.0.2.3") {
		t.Fatal(string(raw))
	}
	for _, path := range []string{store.managedPath(), filepath.Join(store.profileDir(), "profiles.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("public permissions for %s: %v", path, info.Mode())
		}
	}
}

func TestProfileEscapesPercentPaths(t *testing.T) {
	store, c := profileFixture(t, "machine")
	c.Keys[0].PrivatePath = filepath.Join(t.TempDir(), "key%name with spaces")
	if err := store.Check(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestTransportHostScope(t *testing.T) {
	c := Connection{Alias: "bivouac-one", PreferredHost: "192.0.2.1", PublicIP: "192.0.2.2", User: "me"}
	ctx := WithConnection(context.Background(), c)
	if host, err := TransportHost(ctx, "192.0.2.1", "me"); err != nil || host != c.Alias {
		t.Fatalf("host=%q err=%v", host, err)
	}
	if _, err := TransportHost(ctx, "github.com", "me"); err == nil {
		t.Fatal("unrelated host received identity")
	}
	if host, err := TransportHost(context.Background(), "github.com", "git"); err != nil || host != "github.com" {
		t.Fatal("legacy changed")
	}
}

func TestProfileUseIAP_RendersProxyCommand(t *testing.T) {
	store, c := profileFixture(t, "iapvm")
	c.UseIAP = true
	rendered, err := renderProfile(c)
	if err != nil {
		t.Fatalf("renderProfile failed: %v", err)
	}
	if !strings.Contains(rendered, "ProxyCommand bivouac proxy iapvm") {
		t.Fatalf("expected ProxyCommand bivouac proxy iapvm, got: %s", rendered)
	}
	if strings.Contains(rendered, "HostName") {
		t.Fatalf("unexpected HostName in IAP profile: %s", rendered)
	}
	if err := store.Check(context.Background(), c); err != nil {
		t.Fatalf("store.Check failed: %v", err)
	}
	if err := store.Upsert(context.Background(), c); err != nil {
		t.Fatalf("store.Upsert failed: %v", err)
	}
}
