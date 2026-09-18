package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/testenv"
)

// minimalBivouacPkl writes a valid bivouac.pkl into dir, real enough
// for config.Resolve to evaluate with the pkl CLI.
//
// sshKeys is set so tests exercising something other than the SSH-key
// question -- most of them -- don't trip ensureSSHKeys' refusal, which
// runs before any of them reach the check they actually mean to test.
func minimalBivouacPkl(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "bivouac.pkl")
	body := strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
		`sshKeys { "aa:bb:cc" }`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// chdir switches the process's working directory to dir for the
// duration of the test, restoring it afterward.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// initTestRepo creates a fresh git repo in a temp dir and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	return dir
}

func TestUpCommand_NotInRepoErrors(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "use --repo") {
		t.Errorf("error = %q, want mention of --repo", err.Error())
	}
}

func TestUpCommand_MissingTokenErrors(t *testing.T) {
	dir := initTestRepo(t)
	chdir(t, dir)
	minimalBivouacPkl(t, dir)
	testenv.Isolate(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DIGITALOCEAN_TOKEN") {
		t.Errorf("error = %q, want mention of DIGITALOCEAN_TOKEN", err.Error())
	}
}

func TestUpCommand_DeclinedConfirmation_AbortsWithoutCreating(t *testing.T) {
	dir := initTestRepo(t)
	chdir(t, dir)
	minimalBivouacPkl(t, dir)
	t.Setenv("DIGITALOCEAN_TOKEN", "test-token")

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	root.SetIn(strings.NewReader("n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil (declining isn't an error)", err)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("output = %q, want it to mention the abort", out.String())
	}
	if strings.Contains(out.String(), "is up") {
		t.Errorf("output = %q, want no confirmation the instance was created", out.String())
	}
}

func TestUpCommand_ConfirmationSummary_ShownBeforePrompt(t *testing.T) {
	dir := initTestRepo(t)
	chdir(t, dir)
	minimalBivouacPkl(t, dir)
	t.Setenv("DIGITALOCEAN_TOKEN", "test-token")

	root := newRootCmd()
	root.SetArgs([]string{"up"})
	root.SetIn(strings.NewReader("n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"nyc3", "s-1vcpu-1gb", "python", "Proceed?"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to contain %q", out.String(), want)
		}
	}
}

func TestUpCommand_PositionalNameOverridesDerivedName(t *testing.T) {
	dir := initTestRepo(t)
	chdir(t, dir)
	minimalBivouacPkl(t, dir)
	testenv.Isolate(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"up", "somename"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `instance "somename"`) {
		t.Errorf("error = %q, want it to name instance %q, not the derived repo name", err.Error(), "somename")
	}
}

// up and provision need the repository root regardless of how the instance
// name was resolved -- bivouac.pkl lives in it, and lifecycle.Up takes it.
// So unlike identity.InstanceName, which returns a positional name without
// ever looking for a repository, these two must still fail outside one even
// when the name was given explicitly.
//
// Nothing pinned this: the existing not-in-repo tests pass no positional
// argument, so they would keep passing against a version that short-circuits
// on the name and then dereferences a root it never resolved.
func TestUpCommand_NotInRepoErrorsEvenWithAnExplicitName(t *testing.T) {
	for _, args := range [][]string{
		{"up", "somename"},
		{"up", "--name", "somename"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			chdir(t, t.TempDir())

			root := newRootCmd()
			root.SetArgs(args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected an error: bivouac.pkl cannot be found without a repository root")
			}
			if !strings.Contains(err.Error(), "use --repo") {
				t.Errorf("error = %q, want mention of --repo", err.Error())
			}
		})
	}
}

// noSSHKeysBivouacPkl writes a resolvable bivouac.pkl with no sshKeys
// field, the exact shape that let `up` create an unreachable droplet
// before ensureSSHKeys existed.
func noSSHKeysBivouacPkl(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "bivouac.pkl")
	body := strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// noopKeySources offers nothing local and no provider account, for
// tests that only care about the empty-choice refusal.
func noopKeySources() keySources {
	return keySources{
		local:    func() ([]sshkeys.Key, error) { return nil, nil },
		registry: func(ctx context.Context) (provider.KeyRegistry, error) { return nil, nil },
	}
}

func TestEnsureSSHKeys_AlreadyConfigured_ReturnsCfgUnchanged(t *testing.T) {
	keys := []string{"aa:bb"}
	cfg := config.Resolved{Config: config.Config{SshKeys: &keys}}

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	got, err := ensureSSHKeys(cmd, cfg, "/unused/bivouac.pkl")
	if err != nil {
		t.Fatalf("ensureSSHKeys() error = %v", err)
	}
	if got.SshKeys == nil || !reflect.DeepEqual(*got.SshKeys, keys) {
		t.Errorf("SshKeys = %v, want unchanged %v", got.SshKeys, keys)
	}
}

// Without a terminal, asking is indistinguishable from a hang -- so a
// missing key must refuse the same way a missing bivouac.pkl does,
// not silently proceed as it did before this guard existed.
func TestEnsureSSHKeys_NoneConfiguredNoTerminal_Refuses(t *testing.T) {
	cfg := config.Resolved{}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	_, err := ensureSSHKeys(cmd, cfg, "/unused/bivouac.pkl")
	if err == nil {
		t.Fatal("ensureSSHKeys() error = nil, want a refusal with no terminal to ask on")
	}
	if !strings.Contains(err.Error(), "no SSH keys configured") {
		t.Errorf("error = %q, want it to name the missing sshKeys", err.Error())
	}
}

func TestEnsureSSHKeysWith_WritesChosenKeyAndReResolves(t *testing.T) {
	dir := t.TempDir()
	path := noSSHKeysBivouacPkl(t, dir)
	testenv.Isolate(t)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())

	s := newScript()
	s.keyChoice = []string{"aa:bb"}
	sources := keySources{
		local: func() ([]sshkeys.Key, error) {
			return []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent)}, nil
		},
		registry: func(ctx context.Context) (provider.KeyRegistry, error) { return nil, nil },
	}

	cfg, err := ensureSSHKeysWith(cmd, path, s, sources)
	if err != nil {
		t.Fatalf("ensureSSHKeysWith() error = %v\n%s", err, out.String())
	}
	if cfg.SshKeys == nil || !reflect.DeepEqual(*cfg.SshKeys, []string{"aa:bb"}) {
		t.Errorf("resolved SshKeys = %v, want [aa:bb]", cfg.SshKeys)
	}

	basePath, err := config.DefaultBasePath()
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("reading %s: %v", basePath, err)
	}
	if !strings.Contains(string(written), "aa:bb") {
		t.Errorf("base.pkl = %s, want it to record the chosen key", written)
	}
}

// The one failure ensureSSHKeys exists to prevent must still be
// possible to choose explicitly -- but only after being asked and
// warned, never silently as it was before.
func TestEnsureSSHKeysWith_NoKeysChosen_RefusesRatherThanProceeding(t *testing.T) {
	dir := t.TempDir()
	path := noSSHKeysBivouacPkl(t, dir)
	testenv.Isolate(t)

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	s := newScript()
	s.keyChoice = []string{}

	_, err := ensureSSHKeysWith(cmd, path, s, noopKeySources())
	if err == nil || !strings.Contains(err.Error(), "no SSH keys selected") {
		t.Errorf("ensureSSHKeysWith() error = %v, want a refusal naming no keys selected", err)
	}
}
