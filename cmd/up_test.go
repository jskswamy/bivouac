package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// minimalCloudlabPkl writes a valid cloudlab.pkl into dir, real enough
// for config.Resolve to evaluate with the pkl CLI.
func minimalCloudlabPkl(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "cloudlab.pkl")
	body := strings.Join([]string{
		`region = "nyc3"`,
		`size = "s-1vcpu-1gb"`,
		`template = "python"`,
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
	chdir(t, initTestRepo(t))
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
	minimalCloudlabPkl(t, dir)
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
	minimalCloudlabPkl(t, dir)
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
	chdir(t, initTestRepo(t))
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
