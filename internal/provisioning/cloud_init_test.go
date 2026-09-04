package provisioning

import (
	"strings"
	"testing"
)

func TestRenderCloudInit_InstallsNixNonInteractively(t *testing.T) {
	got, err := RenderCloudInit("devuser")
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
	got, err := RenderCloudInit("devuser")
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
	got, err := RenderCloudInit("devuser")
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
	got, err := RenderCloudInit("devuser")
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
	got, err := RenderCloudInit("devuser")
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
		if _, err := RenderCloudInit(bad); err == nil {
			t.Errorf("RenderCloudInit(%q) error = nil, want an error for an invalid username", bad)
		}
	}
}
