package provisioning

import "testing"

func TestResolveTemplateRef_BuiltinExpandsWithArchSuffix(t *testing.T) {
	got := ResolveTemplateRef("python", "x86_64")
	want := "github:jskswamy/cloudlab?dir=templates#python-x86_64-linux"
	if got != want {
		t.Errorf("ResolveTemplateRef(python, x86_64) = %q, want %q", got, want)
	}
}

func TestResolveTemplateRef_BuiltinArm64(t *testing.T) {
	got := ResolveTemplateRef("docker", "arm64")
	want := "github:jskswamy/cloudlab?dir=templates#docker-aarch64-linux"
	if got != want {
		t.Errorf("ResolveTemplateRef(docker, arm64) = %q, want %q", got, want)
	}
}

func TestResolveTemplateRef_ByoRefPassesThroughUnchanged(t *testing.T) {
	byo := "github:someuser/their-templates#custom"
	got := ResolveTemplateRef(byo, "x86_64")
	if got != byo {
		t.Errorf("ResolveTemplateRef(%q) = %q, want unchanged", byo, got)
	}
}

func TestNixSystem_DefaultsToX86_64Linux(t *testing.T) {
	if got := NixSystem("x86_64"); got != "x86_64-linux" {
		t.Errorf("NixSystem(x86_64) = %q, want x86_64-linux", got)
	}
	if got := NixSystem(""); got != "x86_64-linux" {
		t.Errorf("NixSystem(\"\") = %q, want x86_64-linux", got)
	}
}

func TestNixSystem_Arm64(t *testing.T) {
	if got := NixSystem("arm64"); got != "aarch64-linux" {
		t.Errorf("NixSystem(arm64) = %q, want aarch64-linux", got)
	}
}

func TestSplitFlakeRef_SplitsOnLastHash(t *testing.T) {
	url, name := splitFlakeRef("github:jskswamy/cloudlab?dir=templates#python-x86_64-linux")
	if url != "github:jskswamy/cloudlab?dir=templates" {
		t.Errorf("url = %q, want %q", url, "github:jskswamy/cloudlab?dir=templates")
	}
	if name != "python-x86_64-linux" {
		t.Errorf("name = %q, want %q", name, "python-x86_64-linux")
	}
}

func TestSplitFlakeRef_NoHashReturnsEmptyName(t *testing.T) {
	url, name := splitFlakeRef("github:someuser/their-templates")
	if url != "github:someuser/their-templates" || name != "" {
		t.Errorf("splitFlakeRef(no-hash) = (%q, %q), want (unchanged, empty)", url, name)
	}
}
