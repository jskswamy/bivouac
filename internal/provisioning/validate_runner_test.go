package provisioning

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
)

// fakeNix answers without a nix on the machine running the test, which
// is the point: what Validate checks, and how it reports, is decided
// here rather than by whatever nixpkgs happens to be current.
type fakeNix struct {
	// missing are the "<ref>#<attr>" targets that do not resolve.
	missing map[string]bool
	// asked records every target, in order.
	asked []string
}

func (f *fakeNix) RunNix(ctx context.Context, args ...string) ([]byte, error) {
	var target string
	for i, a := range args {
		if i > 0 && args[i-1] == "--impure" {
			target = a
			break
		}
	}
	f.asked = append(f.asked, target)
	if f.missing[target] {
		return []byte(fmt.Sprintf("error: attribute '%s' missing\n", target)), fmt.Errorf("exit status 1")
	}
	return []byte("\"ok\"\n"), nil
}

func templateCfg(t *testing.T) config.Resolved {
	t.Helper()
	return config.Resolved{
		Template: "path:/somewhere#default",
		Config:   config.Config{Arch: "x86_64"},
	}
}

func TestValidate_ChecksPackagesAgainstTheNixpkgsTheFlakePins(t *testing.T) {
	cfg := templateCfg(t)
	cfg.Packages = []string{"ripgrep", "jq"}

	fake := &fakeNix{}
	if err := Validate(context.Background(), fake, cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, pkg := range cfg.Packages {
		want := NixpkgsRef + "#legacyPackages.x86_64-linux." + pkg
		if !contains(fake.asked, want) {
			t.Errorf("did not check %q; asked %v", want, fake.asked)
		}
	}
}

func TestValidate_PackagesFollowArch(t *testing.T) {
	cfg := templateCfg(t)
	cfg.Arch = "arm64"
	cfg.Packages = []string{"ripgrep"}

	fake := &fakeNix{}
	if err := Validate(context.Background(), fake, cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	want := NixpkgsRef + "#legacyPackages.aarch64-linux.ripgrep"
	if !contains(fake.asked, want) {
		t.Errorf("did not check %q; asked %v", want, fake.asked)
	}
}

// The message has to name the package the user typed. A raw Nix trace
// makes them go looking for which of their entries it meant.
func TestValidate_MissingPackageNamesIt(t *testing.T) {
	cfg := templateCfg(t)
	cfg.Packages = []string{"ripgrepp"}

	fake := &fakeNix{missing: map[string]bool{
		NixpkgsRef + "#legacyPackages.x86_64-linux.ripgrepp": true,
	}}
	err := Validate(context.Background(), fake, cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want the typo reported")
	}
	if !strings.Contains(err.Error(), `package "ripgrepp"`) {
		t.Errorf("error = %q, want it to name the package", err)
	}
	if !strings.Contains(err.Error(), "not in nixpkgs") {
		t.Errorf("error = %q, want it to say what is wrong", err)
	}
}

// One round of fixing, not three.
func TestValidate_ReportsEveryProblem(t *testing.T) {
	cfg := templateCfg(t)
	cfg.Packages = []string{"aaa", "bbb"}
	cfg.Flakes = []config.Flake{{Url: "github:foo/bar", Packages: []string{"ccc"}}}

	fake := &fakeNix{missing: map[string]bool{
		NixpkgsRef + "#legacyPackages.x86_64-linux.aaa": true,
		NixpkgsRef + "#legacyPackages.x86_64-linux.bbb": true,
		"github:foo/bar#packages.x86_64-linux.ccc":      true,
	}}
	err := Validate(context.Background(), fake, cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want three problems")
	}
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %q:\n%v", name, err)
		}
	}
}

// agents is a closed set, which stops typos but not nixpkgs renaming
// the package one of them maps to.
func TestValidate_ChecksAgentPackagesUnderTheirNixpkgsNames(t *testing.T) {
	cfg := templateCfg(t)
	cfg.Agents = []string{"claude", "pi"}

	fake := &fakeNix{}
	if err := Validate(context.Background(), fake, cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	for _, attr := range []string{"claude-code", "pi-coding-agent"} {
		want := NixpkgsRef + "#legacyPackages.x86_64-linux." + attr
		if !contains(fake.asked, want) {
			t.Errorf("did not check %q; asked %v", want, fake.asked)
		}
	}
}

func TestValidate_MissingAgentPackageNamesIt(t *testing.T) {
	cfg := templateCfg(t)
	cfg.Agents = []string{"claude"}

	fake := &fakeNix{missing: map[string]bool{
		NixpkgsRef + "#legacyPackages.x86_64-linux.claude-code": true,
	}}
	err := Validate(context.Background(), fake, cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want the missing agent package reported")
	}
	if !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("error = %q, want it to name the nixpkgs attribute", err)
	}
}

// A config with a template and nothing else still checks the template,
// and nothing else.
func TestValidate_NothingToCheckBeyondTheTemplate(t *testing.T) {
	fake := &fakeNix{}
	if err := Validate(context.Background(), fake, templateCfg(t)); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(fake.asked) != 1 {
		t.Errorf("asked %v, want just the template", fake.asked)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
