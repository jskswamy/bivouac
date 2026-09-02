package provisioning

import "testing"

func TestCloudInitUserData_InstallsNixNonInteractively(t *testing.T) {
	if CloudInitUserData == "" {
		t.Fatal("CloudInitUserData is empty")
	}
	if CloudInitUserData[:2] != "#!" {
		t.Errorf("CloudInitUserData = %q, want it to start with a shebang", CloudInitUserData[:2])
	}
	for _, want := range []string{
		"install.determinate.systems/nix",
		"install --no-confirm",
	} {
		if !containsString(CloudInitUserData, want) {
			t.Errorf("CloudInitUserData does not contain %q", want)
		}
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
