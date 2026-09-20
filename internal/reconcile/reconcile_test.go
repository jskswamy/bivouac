package reconcile

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/jskswamy/bivouac/internal/agentcontext"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
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
	testenv.Isolate(t)
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
	testenv.Isolate(t)

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
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", bivouacPath); err != nil {
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
	testenv.Isolate(t)

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
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`packages { "ripgrep" }`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", bivouacPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if gotWriteCmd == "" {
		t.Fatal("packages set, but no file was shipped")
	}
	if !strings.Contains(gotWriteCmd, "/home/devuser/.cache/bivouac/flake.nix") {
		t.Errorf("write command = %q, want it to target the instance user's remote flake path", gotWriteCmd)
	}
	if !strings.Contains(gotWriteStdin, `pkgs."ripgrep"`) {
		t.Errorf("shipped content = %q, want it to reference the configured package", gotWriteStdin)
	}
	if !strings.Contains(gotSwitchCmd, "path:/home/devuser/.cache/bivouac#default") {
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
	testenv.Isolate(t)

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "building home-manager generation\n", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	if err := Reconcile(ctx, "myinstance", bivouacPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !strings.Contains(out.String(), "building home-manager generation") {
		t.Errorf("attached writer = %q, want it to contain the switch command's live output", out.String())
	}
}

func TestReconcile_InstanceNotFound_ReturnsClearError(t *testing.T) {
	testenv.Isolate(t)

	err := Reconcile(context.Background(), "nosuchinstance", "/unused/bivouac.pkl")
	if err == nil {
		t.Fatal("Reconcile() error = nil, want error for an instance not in state")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "nosuchinstance") {
		t.Errorf("error = %q, want it to name the missing instance", err.Error())
	}
}

func TestReconcile_HomeManagerSwitchFails_ErrorIncludesOutput(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		return "error: attribute 'cli' missing\n", 1
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	err := Reconcile(context.Background(), "myinstance", bivouacPath)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want error for a failed home-manager switch")
	}
	if !strings.Contains(err.Error(), "attribute 'cli' missing") {
		t.Errorf("error = %q, want it to include the remote command's output", err.Error())
	}
}

// The config is checked on the instance before anything is built, so a
// mistyped package name costs a few seconds rather than most of a
// home-manager switch.
func TestReconcile_ValidatesOnTheInstanceBeforeSwitching(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`packages {`,
		`  "ripgrep"`,
		`}`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", bivouacPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	evalAt, switchAt := -1, -1
	for i, cmd := range commands {
		if strings.Contains(cmd, "nix") && strings.Contains(cmd, "eval") && strings.Contains(cmd, "legacyPackages") && strings.Contains(cmd, "ripgrep") {
			evalAt = i
		}
		if strings.Contains(cmd, "home-manager") && strings.Contains(cmd, "switch") {
			switchAt = i
		}
	}
	if evalAt < 0 {
		t.Fatalf("the package was never checked on the instance; commands were %v", commands)
	}
	if switchAt < 0 {
		t.Fatalf("no switch ran; commands were %v", commands)
	}
	if evalAt > switchAt {
		t.Errorf("the check ran after the switch (%d vs %d), which is no check at all", evalAt, switchAt)
	}
	if !strings.Contains(commands[evalAt], "bash -lc") {
		t.Errorf("check command = %q, want a login shell so nix is on PATH", commands[evalAt])
	}
}

// A package that does not resolve stops the run: shipping a flake and
// starting a build that cannot finish wastes minutes to reach the same
// conclusion.
func TestReconcile_BadPackageStopsBeforeAnythingIsShipped(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	switched, fileWritten := false, false
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		switch {
		case strings.Contains(cmd, "ripgrepp"):
			return "error: attribute 'ripgrepp' missing\n", 1
		case strings.Contains(cmd, "cat >"):
			fileWritten = true
		case strings.Contains(cmd, "home-manager"):
			switched = true
		}
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`packages {`,
		`  "ripgrepp"`,
		`}`,
	}, "\n")+"\n")

	err := Reconcile(context.Background(), "myinstance", bivouacPath)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want the bad package reported")
	}
	if !strings.Contains(err.Error(), "ripgrepp") {
		t.Errorf("error = %q, want it to name the package the user typed", err)
	}
	if fileWritten {
		t.Error("a flake was shipped for a config that cannot build")
	}
	if switched {
		t.Error("home-manager switch ran despite the config not resolving")
	}
}

// Delivery belongs on this path as much as on session start: an instance
// the user just ran up or provision against must already carry the
// instructions, without waiting for a session to be created.
func TestReconcile_DeliversInstructionsToEachConfiguredHarness(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	writes := map[string]string{}
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		if strings.Contains(cmd, "cat >") {
			writes[cmd] = string(stdin)
		}
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "workflow.md"), "my own workflow\n")
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`agents { "claude" }`,
		`instructions { "workflow.md" }`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", bivouacPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// An absolute path, not "$HOME/...": WriteFile quotes what it is
	// given, so a shell variable would arrive at the remote shell as a
	// literal and make a directory actually called $HOME.
	var content string
	for cmd, stdin := range writes {
		if strings.Contains(cmd, "'/home/devuser/.claude/CLAUDE.md'") {
			content = stdin
		}
	}
	if content == "" {
		t.Fatalf("no write to the harness's global instruction file; writes = %v", writes)
	}
	if !strings.Contains(content, agentcontext.BeginMarker) {
		t.Errorf("delivered content = %q, want the managed block's markers", content)
	}
	if !strings.Contains(content, "my own workflow") {
		t.Errorf("delivered content = %q, want the user's instructions file in it", content)
	}
}

func TestReconcile_ForwardsAgentDuringSwitch(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	forwarded := make(chan agent.Agent, 2)
	addr := startFakeSSHServerWithAgentForwarding(t,
		func(ag agent.Agent) {
			select {
			case forwarded <- ag:
			default:
			}
		},
		func(cmd string, stdin []byte) (string, uint32) { return "", 0 },
	)
	seedInstance(t, "myinstance", addr)

	dir := t.TempDir()
	bivouacPath := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, bivouacPath, strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n")+"\n")

	if err := Reconcile(context.Background(), "myinstance", bivouacPath); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	select {
	case <-forwarded:
		// Forwarding was requested — channel received an agent
	case <-time.After(2 * time.Second):
		t.Fatal("home-manager switch's session never requested a forwarded agent channel")
	}
}

// seedInstanceWithShell is seedInstance for a record that remembers the
// login shell it was created with. The address is a closed local port, so
// a run that gets as far as connecting fails with a connection error --
// which is how these tests tell "refused up front" from "went ahead".
func seedInstanceWithShell(t *testing.T, name, shell string) {
	t.Helper()
	testenv.Isolate(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-config"))
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(state.Record{Name: name, IP: "127.0.0.1:1", User: "devuser", Shell: shell}); err != nil {
		t.Fatal(err)
	}
}

func shellPkl(t *testing.T, shellLine string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bivouac.pkl")
	writeFixture(t, path, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"python\"\n"+shellLine)
	return path
}

// The boot script is the only thing that ever sets the login shell, so
// editing the field afterwards changes nothing on the instance. Refusing
// is the alternative to a config that claims otherwise -- and it has to
// happen before anything is shipped or run.
func TestReconcile_RefusesAShellChangedSinceCreation(t *testing.T) {
	seedInstanceWithShell(t, "myinstance", "fish")

	err := Reconcile(context.Background(), "myinstance", shellPkl(t, `shell = "zsh"`+"\n"))
	if err == nil {
		t.Fatal("Reconcile() error = nil, want a refusal for a changed shell")
	}
	for _, want := range []string{"fixed at creation", `"fish"`, `"zsh"`, "destroy and recreate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestReconcile_ANewInstanceAndAMatchingShellAreNotRefused(t *testing.T) {
	for name, recorded := range map[string]string{
		"legacy":   "", // created before the field existed
		"matching": "fish",
	} {
		t.Run(name, func(t *testing.T) {
			seedInstanceWithShell(t, "myinstance", recorded)

			err := Reconcile(context.Background(), "myinstance", shellPkl(t, ""))
			if err == nil {
				t.Fatal("Reconcile() error = nil, want a connection error from the closed port")
			}
			if strings.Contains(err.Error(), "fixed at creation") {
				t.Errorf("error = %q, want no shell refusal for a %s record", err, name)
			}
		})
	}
}

// A shell creates its own config the first time it starts: fish 4 writes
// ~/.config/fish/config.fish the moment bivouac runs its first command
// through the login shell. home-manager then refuses to switch --
// "Existing file would be clobbered" -- so the switch has to back such
// files up instead of aborting.
func TestReconcile_SwitchBacksUpFilesItWouldOtherwiseClobber(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	var gotCmd string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		gotCmd = cmd
		return "", 0
	})
	seedInstance(t, "myinstance", addr)

	if err := Reconcile(context.Background(), "myinstance", shellPkl(t, "")); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !strings.Contains(gotCmd, "switch -b bivouac-backup ") {
		t.Errorf("command = %q, want the switch to pass -b bivouac-backup", gotCmd)
	}
}
