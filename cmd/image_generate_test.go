package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const imageTestConfig = `region = "nyc3"` + "\n" + `size = "s-1vcpu-1gb"` + "\n" + `template = "python"` + "\n"

// imageGenerateFixture gives a test its own XDG config home with a
// personal base.pkl, and a cwd outside any git repository.
func imageGenerateFixture(t *testing.T) (basePath, workDir string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	basePath = filepath.Join(dir, "xdg", "bivouac", "base.pkl")
	writeImageConfig(t, basePath, imageTestConfig)
	workDir = filepath.Join(dir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)
	return basePath, workDir
}

func writeImageConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runImageCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestImageGenerate_Metadata(t *testing.T) {
	root := newRootCmd()
	c, _, err := root.Find([]string{"image", "generate"})
	if err != nil || c.Name() != "generate" {
		t.Fatalf("image generate not registered: %v", err)
	}
	meta := metadataFor(c)
	if !meta.SupportsJSON || meta.Network {
		t.Errorf("metadata = %+v, want SupportsJSON and no Network", meta)
	}
	if !slices.Contains(meta.SideEffects, "writes bundle directory") {
		t.Errorf("SideEffects = %v, want writes bundle directory", meta.SideEffects)
	}
	if strings.TrimSpace(c.Example) == "" {
		t.Errorf("image generate has no usage examples")
	}
	for _, flag := range []string{"config", "tag", "output", "json"} {
		if c.Flags().Lookup(flag) == nil {
			t.Errorf("missing --%s", flag)
		}
	}
}

func TestImageGenerate_InManifest(t *testing.T) {
	got := manifestFromCommand(t, []string{"manifest"})
	tool := findTool(t, got, "bivouac_image_generate")
	if !tool.Metadata.JsonOutput {
		t.Errorf("image generate must advertise jsonOutput")
	}
	if _, ok := tool.InputSchema.Properties["tag"]; !ok {
		t.Errorf("image generate input schema missing --tag")
	}
}

func TestImageGenerate_WritesBundleAndPrintsDir(t *testing.T) {
	_, workDir := imageGenerateFixture(t)
	out := filepath.Join(workDir, "bundle")

	stdout, _, err := runImageCmd(t, "image", "generate", "--tag", "my-dev", "--output", out)
	if err != nil {
		t.Fatalf("image generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "image.pkr.hcl")); err != nil {
		t.Errorf("bundle not written: %v", err)
	}
	if !strings.Contains(stdout, out) {
		t.Errorf("stdout does not name %s:\n%s", out, stdout)
	}
}

func TestImageGenerate_DefaultOutput(t *testing.T) {
	_, workDir := imageGenerateFixture(t)

	if _, _, err := runImageCmd(t, "image", "generate", "--tag", "my-dev"); err != nil {
		t.Fatalf("image generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "bivouac-image", "image.pkr.hcl")); err != nil {
		t.Errorf("default ./bivouac-image not written: %v", err)
	}
}

func TestImageGenerate_JSONOnStdoutProgressOnStderr(t *testing.T) {
	basePath, workDir := imageGenerateFixture(t)
	out := filepath.Join(workDir, "bundle")

	stdout, stderr, err := runImageCmd(t, "image", "generate", "--tag", "my-dev", "--output", out, "--json")
	if err != nil {
		t.Fatalf("image generate: %v", err)
	}
	var got struct {
		OutputDir  string `json:"output_dir"`
		ConfigPath string `json:"config_path"`
		ConfigKind string `json:"config_kind"`
		Tag        string `json:"tag"`
		Metadata   struct {
			Tag    string `json:"tag"`
			Region string `json:"region"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if got.OutputDir != out || got.ConfigPath != basePath || got.ConfigKind != "base" || got.Tag != "my-dev" {
		t.Errorf("result = %+v", got)
	}
	if got.Metadata.Tag != "my-dev" || got.Metadata.Region != "nyc3" {
		t.Errorf("metadata = %+v", got.Metadata)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Errorf("no progress on stderr")
	}
}

func TestImageGenerate_NonEmptyOutputFails(t *testing.T) {
	_, workDir := imageGenerateFixture(t)
	out := filepath.Join(workDir, "bundle")
	writeImageConfig(t, filepath.Join(out, "stale"), "x")

	if _, _, err := runImageCmd(t, "image", "generate", "--tag", "my-dev", "--output", out); err == nil {
		t.Fatal("generate into a non-empty directory succeeded")
	}
}

func TestImageGenerate_TagRequired(t *testing.T) {
	_, workDir := imageGenerateFixture(t)

	_, _, err := runImageCmd(t, "image", "generate", "--output", filepath.Join(workDir, "b"))
	if err == nil || !strings.Contains(err.Error(), "tag") {
		t.Fatalf("err = %v, want a usage error naming --tag", err)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "b")); statErr == nil {
		t.Errorf("output written despite the usage error")
	}
}

// Generate is local Pkl validation only: no Packer, Nix, git or
// DigitalOcean token may be needed to produce a bundle.
func TestImageGenerate_NeedsOnlyPkl(t *testing.T) {
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl not installed")
	}
	_, workDir := imageGenerateFixture(t)
	t.Setenv("PATH", filepath.Dir(pkl))
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	if _, _, err := runImageCmd(t, "image", "generate", "--tag", "my-dev", "--output", filepath.Join(workDir, "b")); err != nil {
		t.Fatalf("image generate with only pkl on PATH: %v", err)
	}
}

func TestImageGenerate_ExplicitConfig(t *testing.T) {
	_, workDir := imageGenerateFixture(t)
	custom := filepath.Join(t.TempDir(), "custom.pkl")
	writeImageConfig(t, custom, `region = "sfo3"`+"\n")

	stdout, _, err := runImageCmd(t, "image", "generate", "--config", custom, "--tag", "my-dev", "--output", filepath.Join(workDir, "b"), "--json")
	if err != nil {
		t.Fatalf("image generate: %v", err)
	}
	if !strings.Contains(stdout, `"config_kind": "explicit"`) || !strings.Contains(stdout, `"region": "sfo3"`) {
		t.Errorf("explicit config not used:\n%s", stdout)
	}
}

func TestImageGenerate_RepoPicksProjectConfig(t *testing.T) {
	_, workDir := imageGenerateFixture(t)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	project := filepath.Join(repo, "bivouac.pkl")
	writeImageConfig(t, project, `region = "ams3"`+"\n")

	stdout, _, err := runImageCmd(t, "--repo", repo, "image", "generate", "--tag", "my-dev", "--output", filepath.Join(workDir, "b"), "--json")
	if err != nil {
		t.Fatalf("image generate: %v", err)
	}
	var got struct {
		ConfigPath string `json:"config_path"`
		ConfigKind string `json:"config_kind"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	resolved, _ := filepath.EvalSymlinks(project)
	if got.ConfigKind != "project" || (got.ConfigPath != project && got.ConfigPath != resolved) {
		t.Errorf("selected %s %q, want project %q", got.ConfigKind, got.ConfigPath, project)
	}
}

// --config names the file outright, so a --repo beside it would be
// silently ignored; refuse the pair instead.
func TestImageGenerate_RejectsConfigWithRepo(t *testing.T) {
	_, workDir := imageGenerateFixture(t)
	custom := filepath.Join(t.TempDir(), "custom.pkl")
	writeImageConfig(t, custom, imageTestConfig)

	_, _, err := runImageCmd(t, "--repo", workDir, "image", "generate", "--config", custom, "--tag", "my-dev", "--output", filepath.Join(workDir, "b"))
	if err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("err = %v, want --config/--repo rejected", err)
	}
}

// A bundle belongs to no instance, so --name would be silently ignored.
func TestImageGenerate_RejectsName(t *testing.T) {
	_, workDir := imageGenerateFixture(t)

	_, _, err := runImageCmd(t, "--name", "box", "image", "generate", "--tag", "my-dev", "--output", filepath.Join(workDir, "b"))
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("err = %v, want --name rejected", err)
	}
}
