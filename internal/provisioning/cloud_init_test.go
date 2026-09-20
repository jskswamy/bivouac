package provisioning

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderCloudInit_InstallsNixNonInteractively(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.HasPrefix(got, "#!") {
		t.Errorf("RenderCloudInit() = %q, want it to start with a shebang", got[:2])
	}
	for _, want := range []string{
		"install.determinate.systems/nix",
		"install --no-confirm",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderCloudInit() does not contain %q", want)
		}
	}
}

func TestRenderCloudInit_CreatesUserAndCopiesRootAuthorizedKeys(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.Contains(got, "useradd") || !strings.Contains(got, "devuser") {
		t.Errorf("RenderCloudInit() = %q, want it to create the given user", got)
	}
	if !strings.Contains(got, "/root/.ssh/authorized_keys") || !strings.Contains(got, "/home/devuser/.ssh/authorized_keys") {
		t.Errorf("RenderCloudInit() = %q, want it to copy root's authorized_keys to the new user", got)
	}
}

func TestRenderCloudInit_GrantsPasswordlessSudo(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.Contains(got, "devuser ALL=(ALL) NOPASSWD:ALL") {
		t.Errorf("RenderCloudInit() = %q, want a passwordless sudoers entry for the new user", got)
	}
	if !strings.Contains(got, "/etc/sudoers.d/devuser") {
		t.Errorf("RenderCloudInit() = %q, want the sudoers entry under /etc/sudoers.d/<user>", got)
	}
}

func TestRenderCloudInit_EnablesLingeringForNewUserNotRoot(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.Contains(got, "loginctl enable-linger devuser") {
		t.Errorf("RenderCloudInit() = %q, want lingering enabled for the new user", got)
	}
	if strings.Contains(got, "loginctl enable-linger root") {
		t.Errorf("RenderCloudInit() = %q, want root lingering no longer enabled -- home-manager now runs as the new user", got)
	}
}

func TestRenderCloudInit_DisablesRootLoginAfterConfirmingNewUserKeyAccess(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.Contains(got, "PermitRootLogin no") {
		t.Errorf("RenderCloudInit() = %q, want root SSH login disabled", got)
	}

	guardIdx := strings.Index(got, "-s /home/devuser/.ssh/authorized_keys")
	disableIdx := strings.Index(got, "PermitRootLogin no")
	if guardIdx == -1 {
		t.Fatal("RenderCloudInit() has no guard checking the new user's authorized_keys is non-empty before disabling root")
	}
	if disableIdx < guardIdx {
		t.Errorf("PermitRootLogin no appears before the authorized_keys guard -- must be disabled only after confirming key access")
	}

	// Disabling root must be the LAST substantive step -- nothing else
	// the instance depends on (sudo grant, lingering) should come after
	// root access is cut off.
	if idx := strings.Index(got, "NOPASSWD:ALL"); idx > disableIdx {
		t.Error("sudoers grant happens after root login is disabled, want it before")
	}
	if idx := strings.Index(got, "enable-linger devuser"); idx > disableIdx {
		t.Error("lingering is enabled after root login is disabled, want it before")
	}
}

func TestRenderCloudInit_RejectsInvalidUsername(t *testing.T) {
	for _, bad := range []string{"", "Root", "has spaces", "semi;colon", "1startswithdigit"} {
		if _, err := RenderCloudInit(bad, "bash"); err == nil {
			t.Errorf("RenderCloudInit(%q) error = nil, want an error for an invalid username", bad)
		}
	}
}

func TestRenderCloudInit_FishComesFromAptAndIsChosenOnlyIfItRuns(t *testing.T) {
	got, err := RenderCloudInit("devuser", "fish")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	for _, want := range []string{
		"apt-get",
		"install -y -qq fish",
		`grep -qx "$WANT_SHELL" /etc/shells`,
		`"$WANT_SHELL" -c true`,
		`--shell "$LOGIN_SHELL"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderCloudInit() does not contain %q", want)
		}
	}
	if !strings.Contains(got, "WANT_SHELL=/usr/bin/fish") {
		t.Errorf("RenderCloudInit() = %q, want fish at /usr/bin/fish, the apt path, not a Nix profile path", got)
	}
}

// The shell has to be installed before useradd points the account at it.
func TestRenderCloudInit_InstallsTheShellBeforeCreatingTheUser(t *testing.T) {
	got, err := RenderCloudInit("devuser", "fish")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	install, useradd := strings.Index(got, "apt-get"), strings.Index(got, "useradd")
	if install < 0 || useradd < 0 || install > useradd {
		t.Errorf("apt-get at %d, useradd at %d; want the install first", install, useradd)
	}
}

func TestRenderCloudInit_ZshUsesItsOwnAptPath(t *testing.T) {
	got, err := RenderCloudInit("devuser", "zsh")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.Contains(got, "WANT_SHELL=/usr/bin/zsh") || !strings.Contains(got, "install -y -qq zsh") {
		t.Errorf("RenderCloudInit() = %q, want zsh installed and chosen at /usr/bin/zsh", got)
	}
}

func TestRenderCloudInit_BashInstallsNothingAndStaysOnBash(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if strings.Contains(got, "apt-get") {
		t.Errorf("RenderCloudInit() = %q, want no package install for bash", got)
	}
	if !strings.Contains(got, "LOGIN_SHELL=/bin/bash") {
		t.Errorf("RenderCloudInit() = %q, want the login shell left at /bin/bash", got)
	}
}

// The value is interpolated into a root-run boot script, so anything
// outside the three shells is refused here as well as in the schema -- and
// an empty one too, so a config that lost the field on the way fails
// loudly instead of quietly producing a bash instance.
func TestRenderCloudInit_RejectsAnythingButTheThreeShells(t *testing.T) {
	for _, bad := range []string{"", "tcsh", "fish; rm -rf /", "FISH", "/bin/sh"} {
		if _, err := RenderCloudInit("devuser", bad); err == nil {
			t.Errorf("RenderCloudInit(shell=%q) error = nil, want an error", bad)
		}
	}
}

func TestRenderCloudInit_IsValidBashForEveryShell(t *testing.T) {
	for _, shell := range []string{"fish", "zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			got, err := RenderCloudInit("devuser", shell)
			if err != nil {
				t.Fatalf("RenderCloudInit() error = %v", err)
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(got)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n rejected the script: %v\n%s", err, out)
			}
		})
	}
}

// The property that keeps an instance reachable: the script runs under
// set -e, so a failed install must not abort it (before the user exists it
// would lock everyone out) and must leave the account on bash. Runs the
// rendered selection block with an apt-get that fails.
func TestRenderCloudInit_AFailedInstallLeavesTheAccountOnBash(t *testing.T) {
	got, err := RenderCloudInit("devuser", "fish")
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	start := strings.Index(got, "# --- login shell")
	end := strings.Index(got, "# --- end login shell")
	if start < 0 || end < start {
		t.Fatalf("RenderCloudInit() has no login-shell block markers:\n%s", got)
	}
	block := got[start:end]

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "apt-get"), []byte("#!/bin/sh\nexit 100\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\n" + block + "\necho \"LOGIN_SHELL=$LOGIN_SHELL\"\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the block aborted under set -e when apt-get failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "LOGIN_SHELL=/bin/bash") {
		t.Errorf("output = %q, want the account left on /bin/bash", out)
	}
}
