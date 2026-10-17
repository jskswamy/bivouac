package provisioning

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderCloudInit_InstallsNixNonInteractively(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash", BootStock)
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
	got, err := RenderCloudInit("devuser", "bash", BootStock)
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
	got, err := RenderCloudInit("devuser", "bash", BootStock)
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
	got, err := RenderCloudInit("devuser", "bash", BootStock)
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
	got, err := RenderCloudInit("devuser", "bash", BootStock)
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
		if _, err := RenderCloudInit(bad, "bash", BootStock); err == nil {
			t.Errorf("RenderCloudInit(%q) error = nil, want an error for an invalid username", bad)
		}
	}
}

func TestRenderCloudInit_FishComesFromAptAndIsChosenOnlyIfItRuns(t *testing.T) {
	got, err := RenderCloudInit("devuser", "fish", BootStock)
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
	got, err := RenderCloudInit("devuser", "fish", BootStock)
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	install, useradd := strings.Index(got, "apt-get"), strings.Index(got, "useradd")
	if install < 0 || useradd < 0 || install > useradd {
		t.Errorf("apt-get at %d, useradd at %d; want the install first", install, useradd)
	}
}

func TestRenderCloudInit_ZshUsesItsOwnAptPath(t *testing.T) {
	got, err := RenderCloudInit("devuser", "zsh", BootStock)
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	if !strings.Contains(got, "WANT_SHELL=/usr/bin/zsh") || !strings.Contains(got, "install -y -qq zsh") {
		t.Errorf("RenderCloudInit() = %q, want zsh installed and chosen at /usr/bin/zsh", got)
	}
}

func TestRenderCloudInit_BashInstallsNothingAndStaysOnBash(t *testing.T) {
	got, err := RenderCloudInit("devuser", "bash", BootStock)
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
		if _, err := RenderCloudInit("devuser", bad, BootStock); err == nil {
			t.Errorf("RenderCloudInit(shell=%q) error = nil, want an error", bad)
		}
	}
}

func TestRenderCloudInit_IsValidBashForEveryShell(t *testing.T) {
	for _, shell := range []string{"fish", "zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			got, err := RenderCloudInit("devuser", shell, BootStock)
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
	got, err := RenderCloudInit("devuser", "fish", BootStock)
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

// Ubuntu 24.04 ships fish 3.7, while the config that runs under it targets
// fish 4, so fish comes from the release-4 PPA -- which tracks upstream and
// is the same 4.x the Nix profile carries. Only fish: zsh has no such gap.
func TestRenderCloudInit_FishComesFromTheFish4PPA(t *testing.T) {
	got, err := RenderCloudInit("devuser", "fish", BootStock)
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	ppa := strings.Index(got, "ppa:fish-shell/release-4")
	install := strings.Index(got, "install -y -qq fish")
	if ppa < 0 || install < 0 || ppa > install {
		t.Errorf("PPA at %d, install at %d; want the PPA added before fish is installed", ppa, install)
	}
	for _, shell := range []string{"zsh", "bash"} {
		other, err := RenderCloudInit("devuser", shell, BootStock)
		if err != nil {
			t.Fatalf("RenderCloudInit(%s) error = %v", shell, err)
		}
		if strings.Contains(other, "ppa:") {
			t.Errorf("RenderCloudInit(%s) adds a PPA, want it only for fish", shell)
		}
	}
}

// A PPA that cannot be added must not cost the instance fish altogether or
// abort the script: fall back to the distribution's fish, and never let the
// failure escape set -e.
func TestRenderCloudInit_AFailedPPAFallsBackWithoutAbortingTheScript(t *testing.T) {
	got, err := RenderCloudInit("devuser", "fish", BootStock)
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	start := strings.Index(got, "# --- login shell")
	end := strings.Index(got, "# --- end login shell")
	block := got[start:end]

	bin := t.TempDir()
	for _, name := range []string{"add-apt-repository", "apt-get"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 100\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+block+"\necho \"LOGIN_SHELL=$LOGIN_SHELL\"\n")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the block aborted under set -e when the PPA and apt failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "LOGIN_SHELL=/bin/bash") {
		t.Errorf("output = %q, want the account left on /bin/bash", out)
	}
}

// Splitting the software bootstrap out of the boot script must not change
// what a stock launch runs. The golden files are the script as it was
// rendered before the split, so any byte that moves is a behaviour change
// to argue for, not a refactor.
func TestRenderCloudInit_StockUnchanged(t *testing.T) {
	for _, shell := range []string{"fish", "zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "cloud-init-stock-"+shell+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := RenderCloudInit("alice", shell, BootStock)
			if err != nil {
				t.Fatalf("RenderCloudInit() error = %v", err)
			}
			if got != string(want) {
				t.Errorf("stock render drifted from the pre-split script\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// A baked image already carries Nix and the login shell, so its boot path
// installs nothing -- but it still has to create the real account and its
// access, in the same order as a stock boot, and still disable root last.
func TestRenderCloudInit_BakedSkipsInstall(t *testing.T) {
	for _, shell := range []string{"fish", "zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			got, err := RenderCloudInit("alice", shell, BootBaked)
			if err != nil {
				t.Fatalf("RenderCloudInit() error = %v", err)
			}
			if !strings.HasPrefix(got, "#!/bin/bash\nset -euo pipefail\n") {
				t.Errorf("baked render does not start with the shebang and strict mode:\n%s", got)
			}
			for _, banned := range []string{"install.determinate.systems", "apt-get install", "apt-get", "add-apt-repository"} {
				if strings.Contains(got, banned) {
					t.Errorf("baked render contains %q, want no software installation", banned)
				}
			}
			last := -1
			for _, step := range []string{
				"useradd --create-home --shell \"$LOGIN_SHELL\" alice",
				"install -m 0600 -o alice -g alice /root/.ssh/authorized_keys /home/alice/.ssh/authorized_keys",
				"alice ALL=(ALL) NOPASSWD:ALL",
				"loginctl enable-linger alice",
				"if [ -s /home/alice/.ssh/authorized_keys ]; then",
				"PermitRootLogin no",
			} {
				idx := strings.Index(got, step)
				if idx < 0 {
					t.Fatalf("baked render is missing %q:\n%s", step, got)
				}
				if idx < last {
					t.Errorf("%q is out of order in the baked render", step)
				}
				last = idx
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(got)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n rejected the baked script: %v\n%s", err, out)
			}
		})
	}
}

// The baked boot path only detects the shell the image was built with. It
// must point the account at it only when the binary is really there and
// registered, so an image that somehow lacks it still boots onto bash
// rather than an account no one can log into.
func TestRenderCloudInit_BakedPicksTheShellOnlyWhenItIsPresent(t *testing.T) {
	got, err := RenderCloudInit("alice", "fish", BootBaked)
	if err != nil {
		t.Fatalf("RenderCloudInit() error = %v", err)
	}
	start := strings.Index(got, "# --- login shell")
	end := strings.Index(got, "# --- end login shell")
	if start < 0 || end < start {
		t.Fatalf("baked render has no login-shell block markers:\n%s", got)
	}
	block := got[start:end]
	for _, want := range []string{"WANT_SHELL=/usr/bin/fish", `[ -x "$WANT_SHELL" ]`, `grep -qx "$WANT_SHELL" /etc/shells`} {
		if !strings.Contains(block, want) {
			t.Errorf("baked login-shell block does not contain %q:\n%s", want, block)
		}
	}
	if !strings.Contains(got, "useradd --create-home --shell \"$LOGIN_SHELL\" alice") {
		t.Error("baked render does not hand the detected shell to useradd")
	}

	// /usr/bin/fish does not exist on the test host as a registered shell
	// we control, so run the block as-is and expect it to settle on bash
	// without tripping set -e. A stub grep makes the /etc/shells check fail
	// even on a host that has fish.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "grep"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+block+"\necho \"LOGIN_SHELL=$LOGIN_SHELL\"\n")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the baked block aborted under set -e: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "LOGIN_SHELL=/bin/bash") {
		t.Errorf("output = %q, want the account left on /bin/bash", out)
	}

	bash, err := RenderCloudInit("alice", "bash", BootBaked)
	if err != nil {
		t.Fatalf("RenderCloudInit(bash) error = %v", err)
	}
	if strings.Contains(bash, "WANT_SHELL") || !strings.Contains(bash, "LOGIN_SHELL=/bin/bash") {
		t.Errorf("baked bash render = %q, want plain bash with no detection", bash)
	}
}

func TestRenderCloudInit_RejectsInvalidInputInBothModes(t *testing.T) {
	for _, mode := range []BootMode{BootStock, BootBaked} {
		if _, err := RenderCloudInit("alice", "tcsh", mode); err == nil {
			t.Errorf("mode %d: RenderCloudInit(shell=tcsh) error = nil, want an error", mode)
		}
		if _, err := RenderCloudInit("Root", "bash", mode); err == nil {
			t.Errorf("mode %d: RenderCloudInit(user=Root) error = nil, want an error", mode)
		}
	}
}

// An unknown mode is a programming error; rendering it as either path would
// silently pick a boot behaviour no caller asked for.
func TestRenderCloudInit_RejectsAnUnknownMode(t *testing.T) {
	if _, err := RenderCloudInit("alice", "bash", BootMode(99)); err == nil {
		t.Error("RenderCloudInit(mode=99) error = nil, want an error")
	}
}

// The bundle builder runs the software bootstrap on its own as user data,
// so it must be a complete script: Nix and the shell, and none of the
// per-instance access setup that belongs to a real launch.
func TestRenderBootstrap(t *testing.T) {
	for _, shell := range []string{"fish", "zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			got, err := RenderBootstrap(shell)
			if err != nil {
				t.Fatalf("RenderBootstrap() error = %v", err)
			}
			if !strings.HasPrefix(got, "#!/bin/bash\nset -euo pipefail\n") {
				t.Errorf("RenderBootstrap() does not start with the shebang and strict mode:\n%s", got)
			}
			if !strings.Contains(got, "install.determinate.systems/nix") {
				t.Error("RenderBootstrap() does not install Nix")
			}
			if shell != "bash" && !strings.Contains(got, "install -y -qq "+shell) {
				t.Errorf("RenderBootstrap() does not install %s", shell)
			}
			for _, banned := range []string{"useradd", "authorized_keys", "sshd_config"} {
				if strings.Contains(got, banned) {
					t.Errorf("RenderBootstrap() contains %q, want no per-instance access setup", banned)
				}
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(got)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n rejected the bootstrap: %v\n%s", err, out)
			}
		})
	}
	for _, bad := range []string{"", "tcsh", "fish; rm -rf /"} {
		if _, err := RenderBootstrap(bad); err == nil {
			t.Errorf("RenderBootstrap(%q) error = nil, want an error", bad)
		}
	}
}

// Ubuntu 24.04 starts sshd through ssh.socket, so until someone first
// connects there is no /run/sshd -- which `sshd -t` needs -- and no
// active ssh.service to reload. A baked launch reaches this block within
// seconds of boot, before any connection, so both must work then.
func TestRenderCloudInit_RootDisableWorksBeforeSSHDHasRun(t *testing.T) {
	for _, mode := range []BootMode{BootStock, BootBaked} {
		got, err := RenderCloudInit("devuser", "bash", mode)
		if err != nil {
			t.Fatalf("RenderCloudInit(%v) error = %v", mode, err)
		}
		mkdir, check := strings.Index(got, "mkdir -p /run/sshd"), strings.Index(got, "sshd -t")
		if mkdir < 0 || mkdir > check {
			t.Errorf("mode %v: /run/sshd is not created before sshd -t", mode)
		}
		if strings.Contains(got, "systemctl reload ") {
			t.Errorf("mode %v: a plain reload fails while ssh.service is inactive under socket activation", mode)
		}
		if !strings.Contains(got, "try-reload-or-restart") {
			t.Errorf("mode %v: want systemctl try-reload-or-restart, which succeeds when sshd is not running", mode)
		}
	}
}
