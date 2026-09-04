package reconcile

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/state"
)

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

// seedInstance points state at a temp dir, records a Record for name
// whose IP is the fake SSH server's full "host:port" address (Connect
// only appends its default port when one isn't already present, so
// tests can point it at a fake server's arbitrary port this way), and
// points XDG_CONFIG_HOME at a nonexistent dir so no ambient personal
// base config interferes.
func seedInstance(t *testing.T, name, addr string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(state.Record{Name: name, IP: addr, User: "devuser"}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcile_NoPackages_SwitchesBareTemplateRef_NoFileShipped(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var gotCmd string
	fileWritten := false
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		if strings.Contains(cmd, "cat >") {
			fileWritten = true
		}
		gotCmd = cmd
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", cloudlabPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if fileWritten {
		t.Error("no packages/flakes set, but a file was shipped — should switch the bare template ref instead")
	}
	if !strings.Contains(gotCmd, "home-manager") || !strings.Contains(gotCmd, "switch") {
		t.Errorf("command = %q, want a home-manager switch invocation", gotCmd)
	}
	if !strings.Contains(gotCmd, "python-x86_64-linux") {
		t.Errorf("command = %q, want it to reference the resolved template ref", gotCmd)
	}
	if !strings.Contains(gotCmd, "bash -lc") {
		t.Errorf("command = %q, want it wrapped in a login shell so PATH is set up", gotCmd)
	}
	if !strings.Contains(gotCmd, "--no-write-lock-file") {
		t.Errorf("command = %q, want --no-write-lock-file so template/nixpkgs stay floating", gotCmd)
	}
	if !strings.Contains(gotCmd, "--refresh") {
		t.Errorf("command = %q, want --refresh so a floating template ref isn't served stale from Nix's tarball cache", gotCmd)
	}
	if !strings.Contains(gotCmd, "--impure") {
		t.Errorf("command = %q, want --impure so common.nix can read $USER/$HOME via builtins.getEnv", gotCmd)
	}
}

func TestReconcile_WithPackages_ShipsRenderedFlakeThenSwitchesIt(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var gotWriteCmd, gotWriteStdin, gotSwitchCmd string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		if strings.Contains(cmd, "cat >") {
			gotWriteCmd = cmd
			gotWriteStdin = string(stdin)
		} else {
			gotSwitchCmd = cmd
		}
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`packages { "ripgrep" }`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", cloudlabPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if gotWriteCmd == "" {
		t.Fatal("packages set, but no file was shipped")
	}
	if !strings.Contains(gotWriteCmd, "/home/devuser/.cache/cloudlab/flake.nix") {
		t.Errorf("write command = %q, want it to target the instance user's remote flake path", gotWriteCmd)
	}
	if !strings.Contains(gotWriteStdin, `pkgs."ripgrep"`) {
		t.Errorf("shipped content = %q, want it to reference the configured package", gotWriteStdin)
	}
	if !strings.Contains(gotSwitchCmd, "path:/home/devuser/.cache/cloudlab#default") {
		t.Errorf("switch command = %q, want it to target the shipped flake's default output", gotSwitchCmd)
	}
	if !strings.Contains(gotSwitchCmd, "bash -lc") {
		t.Errorf("switch command = %q, want it wrapped in a login shell so PATH is set up", gotSwitchCmd)
	}
	if !strings.Contains(gotSwitchCmd, "--no-write-lock-file") {
		t.Errorf("switch command = %q, want --no-write-lock-file so template/nixpkgs stay floating", gotSwitchCmd)
	}
	if !strings.Contains(gotSwitchCmd, "--refresh") {
		t.Errorf("switch command = %q, want --refresh so a floating template ref isn't served stale from Nix's tarball cache", gotSwitchCmd)
	}
}

func TestReconcile_StreamsSwitchOutputToAttachedWriter(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "building home-manager generation\n", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	if err := Reconcile(ctx, "myinstance", cloudlabPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !strings.Contains(out.String(), "building home-manager generation") {
		t.Errorf("attached writer = %q, want it to contain the switch command's live output", out.String())
	}
}

func TestReconcile_InstanceNotFound_ReturnsClearError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	err := Reconcile(context.Background(), "nosuchinstance", "/unused/cloudlab.pkl")
	if err == nil {
		t.Fatal("Reconcile() error = nil, want error for an instance not in state")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "nosuchinstance") {
		t.Errorf("error = %q, want it to name the missing instance", err.Error())
	}
}

func TestReconcile_HomeManagerSwitchFails_ErrorIncludesOutput(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "error: attribute 'cli' missing\n", 1
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	cloudlabPath := filepath.Join(dir, "cloudlab.pkl")
	writeFixture(t, cloudlabPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	err := Reconcile(context.Background(), "myinstance", cloudlabPath)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want error for a failed home-manager switch")
	}
	if !strings.Contains(err.Error(), "attribute 'cli' missing") {
		t.Errorf("error = %q, want it to include the remote command's output", err.Error())
	}
}
