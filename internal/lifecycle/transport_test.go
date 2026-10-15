package lifecycle

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/testenv"
	"golang.org/x/crypto/ssh/agent"
)

func transportFixture(t *testing.T) (context.Context, sshidentity.Connection) {
	t.Helper()
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	key := testenv.SSHKey(t)
	ids, err := sshidentity.Select(context.Background(), []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	c := sshidentity.Connection{Name: "machine", Alias: "bivouac-machine", User: "me", PublicIP: "192.0.2.1", PreferredHost: "192.0.2.1", Keys: ids}
	return sshidentity.WithConnection(context.Background(), c), c
}

func TestInstanceGitUsesPersistentAliasAndRealEndpoint(t *testing.T) {
	ctx, c := transportFixture(t)
	gitURL, err := instanceGitURL(ctx, c.User, c.PreferredHost, "/repo with spaces")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitURL, "bivouac-machine") {
		t.Fatalf("remote=%q", gitURL)
	}
	target, err := gitEndpoint(ctx, gitURL)
	if err != nil || target != "192.0.2.1:22" {
		t.Fatalf("endpoint=%q err=%v", target, err)
	}
	if _, err := instanceGitURL(ctx, "git", "github.com", "/repo"); err == nil {
		t.Fatal("unrelated remote received identity")
	}
}

// This fixture streams the real Git upload-pack protocol over actual SSH.
// The older command/response fixtures cannot model Git's bidirectional stream.
func gitSSHServer(t *testing.T, key sshkeys.Key, bare string) string {
	t.Helper()
	return streamSSHServer(t, key, func(command string) *exec.Cmd {
		if !strings.HasPrefix(command, "git-upload-pack ") {
			return nil
		}
		// #nosec G204 -- real Git protocol fixture; bare is a disposable repository.
		return exec.Command("git-upload-pack", bare)
	})
}

func streamSSHServer(t *testing.T, key sshkeys.Key, run func(string) *exec.Cmd) string {
	t.Helper()
	return testenv.StreamSSHServer(t, key, run)
}

func unrelatedTransportAgent(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bivouac-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ring := agent.NewKeyring()
	for i := 0; i < 8; i++ {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := ring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = conn.Close() }(); _ = agent.ServeAgent(ring, conn) }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", socket)
}

func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// #nosec G204 -- Git fixture arguments and directories are owned by this test.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestOrdinaryGitFetchPersistsSelectedIdentity(t *testing.T) {
	ctx, c := transportFixture(t)
	unrelatedTransportAgent(t)
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: c.Keys[0].PublicPath, Path: c.Keys[0].PublicPath, ConfigPath: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	gitFixture(t, source, "init")
	gitFixture(t, source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	bare := filepath.Join(t.TempDir(), "remote repo.git")
	gitFixture(t, source, "clone", "--bare", source, bare)
	c.PreferredHost = gitSSHServer(t, key, bare)
	c.PublicIP = c.PreferredHost
	ctx = sshidentity.WithConnection(ctx, c)
	// Readiness records the real endpoint's host key without bypassing checks.
	client, err := reconcile.Connect(ctx, c.PublicIP, c.User)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	store, err := sshidentity.DefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	remote, err := instanceGitURL(ctx, c.User, c.PreferredHost, bare)
	if err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	gitFixture(t, local, "init")
	gitFixture(t, local, "remote", "add", "bivouac-session", remote)
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	if err := os.Unsetenv("GIT_SSH_COMMAND"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("GIT_SSH"); err != nil {
		t.Fatal(err)
	}
	// -F selects the isolated fixture user config because OpenSSH uses passwd's
	// home, not $HOME. No bivouac wrapper or per-key git override is involved.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("ssh -F %s -o UserKnownHostsFile=%s -o StrictHostKeyChecking=yes", shellQuote(filepath.Join(store.SSHDir, "config")), shellQuote(filepath.Join(home, ".ssh", "known_hosts")))
	gitFixture(t, local, "config", "core.sshCommand", command)
	gitFixture(t, local, "fetch", "bivouac-session")
	want := gitFixture(t, source, "rev-parse", "HEAD")
	if got := gitFixture(t, local, "rev-parse", "FETCH_HEAD"); got != want {
		t.Fatalf("fetched %s want %s", got, want)
	}
	if _, err := runInstanceGit(ctx, local, c.PreferredHost, "fetch", "bivouac-session"); err == nil || !strings.Contains(err.Error(), "core.sshCommand") {
		t.Fatalf("caller config conflict=%v", err)
	}
}

func TestInstanceGitFallbackRetainsSelectedAlias(t *testing.T) {
	ctx, c := transportFixture(t)
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: c.Keys[0].PublicPath, Path: c.Keys[0].PublicPath, ConfigPath: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	gitFixture(t, source, "init")
	gitFixture(t, source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitFixture(t, source, "clone", "--bare", source, bare)
	c.PublicIP = gitSSHServer(t, key, bare)
	c.PreferredHost = "127.0.0.1:1"
	ctx = sshidentity.WithConnection(ctx, c)
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
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = -G ]; then exec " + shellQuote(realSSH) + " \"$@\"; fi\nexec " + shellQuote(realSSH) + " -F " + shellQuote(filepath.Join(profiles.SSHDir, "config")) + " -o " + shellQuote("UserKnownHostsFile="+filepath.Join(home, ".ssh", "known_hosts")) + " -o StrictHostKeyChecking=yes \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	local := t.TempDir()
	gitFixture(t, local, "init")
	remote, err := instanceGitURL(ctx, c.User, c.PreferredHost, bare)
	if err != nil {
		t.Fatal(err)
	}
	gitFixture(t, local, "remote", "add", "bivouac-session", remote)
	gitFixture(t, local, "remote", "add", "origin", "ssh://git@github.com/unrelated/repository")
	if out, err := runInstanceGit(ctx, local, c.PreferredHost, fetchRemoteFastArgs("bivouac-session")...); err != nil {
		t.Fatalf("fallback: %v: %s", err, out)
	}
	if got := gitFixture(t, local, "remote", "get-url", "bivouac-session"); got != remote {
		t.Fatal("fallback rewrote persistent alias")
	}
	if _, err := runInstanceGit(ctx, local, c.PreferredHost, "fetch", "origin"); err == nil {
		t.Fatal("unrelated remote received selected authentication")
	}
	if got := gitFixture(t, local, "remote", "get-url", "origin"); got != "ssh://git@github.com/unrelated/repository" {
		t.Fatal("unrelated remote changed")
	}
	t.Setenv("GIT_SSH_COMMAND", "ssh -i unrelated")
	if _, err := runInstanceGit(ctx, local, c.PublicIP, "fetch", "bivouac-session"); err == nil {
		t.Fatal("caller GIT_SSH_COMMAND silently replaced")
	}
}

func TestInstanceGitRejectsURLRewrite(t *testing.T) {
	ctx, c := transportFixture(t)
	local := t.TempDir()
	gitFixture(t, local, "init")
	remote, err := instanceGitURL(ctx, c.User, c.PreferredHost, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	gitFixture(t, local, "config", "url.ssh://git@github.com/.insteadOf", "ssh://me@bivouac-machine/")
	if _, err := runInstanceGit(ctx, local, c.PublicIP, "push", remote, "HEAD:refs/heads/session"); err == nil || !strings.Contains(err.Error(), "URL rewrite") {
		t.Fatalf("rewrite conflict = %v", err)
	}
}

func TestExternalTransportsShareAlias(t *testing.T) {
	ctx, c := transportFixture(t)
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "args")
	sshScript := "#!/bin/sh\nif [ \"$1\" = -G ]; then exec " + shellQuote(realSSH) + " \"$@\"; fi\nprintf '%s\\n' \"$@\" > " + shellQuote(log) + "\n"
	rsyncScript := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'rsync  version 3.5.0 protocol version 32'; exit 0; fi\nprintf '%s\\n' \"$@\" > " + shellQuote(log) + "\n"
	for name, script := range map[string]string{"ssh": sshScript, "rsync": rsyncScript, "herdr": rsyncScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_ENV", "")
	local := t.TempDir()
	operations := []func() error{
		func() error { return SSH(ctx, c.PublicIP, c.User, "/repo with spaces", true) },
		func() error { return Tmux(ctx, c.PublicIP, c.User, "session", true) },
		func() error { return Pair(ctx, c.PublicIP, "100.64.0.1", c.User, false) },
		func() error { return Forward(ctx, c.PublicIP, c.User, 8080, 8080) },
		func() error { return Push(ctx, c.PublicIP, c.User, local, "/remote with spaces") },
		func() error { return Pull(ctx, c.PublicIP, c.User, "/remote with spaces", local) },
		func() error { return Herdr(ctx, c.PublicIP, c.User, "session") },
	}
	for i, operation := range operations {
		if err := operation(); err != nil {
			t.Fatalf("transport %d: %v", i, err)
		}
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "me@bivouac-machine") {
			t.Fatalf("transport %d lost alias: %s", i, raw)
		}
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
