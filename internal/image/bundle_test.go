package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
)

// selected is a base.pkl-shaped selection for blr1/x86_64/fish whose
// launch image is a tag reference. Its Path is deliberately under
// /home, so the inspection test catches it if it leaks into the bundle.
func selected(mut func(*config.Config)) config.Selected {
	region, size, tmpl := "blr1", "s-2vcpu-4gb", "python"
	c := config.Config{Region: &region, Size: &size, Template: &tmpl, Arch: "x86_64", Image: "tag:my-dev"}
	if mut != nil {
		mut(&c)
	}
	shell := "fish"
	if c.Shell != nil {
		shell = *c.Shell
	}
	return config.Selected{
		Path: "/home/someone/.config/bivouac/base.pkl",
		Kind: config.SelectionBase,
		Config: config.Resolved{
			Config: c, Region: *c.Region, Size: *c.Size, Template: *c.Template, Shell: shell,
		},
	}
}

func bundleOpts(t *testing.T, sel config.Selected) BundleOptions {
	t.Helper()
	return BundleOptions{
		OutputDir:      filepath.Join(t.TempDir(), "bundle"),
		Selected:       sel,
		Tag:            "my-dev",
		Username:       "alice",
		BivouacVersion: "v0.0.0-test",
	}
}

func withPackages(c *config.Config) { c.Packages = []string{"ripgrep"} }

func generate(t *testing.T, opts BundleOptions) Bundle {
	t.Helper()
	b, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return b
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bundleFiles lists every regular file under dir, bundle-relative, with
// its permission bits.
func bundleFiles(t *testing.T, dir string) map[string]fs.FileMode {
	t.Helper()
	got := map[string]fs.FileMode{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		got[filepath.ToSlash(rel)] = info.Mode().Perm()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestGenerate_RefusesNonEmptyOutputDir(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.OutputDir, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(opts); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want a not-empty refusal", err)
	}
	files := bundleFiles(t, opts.OutputDir)
	if len(files) != 1 || readFile(t, filepath.Join(opts.OutputDir, "keep.txt")) != "mine" {
		t.Fatalf("output dir changed: %v", files)
	}
}

func TestGenerate_AcceptsEmptyExistingOutputDir(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	generate(t, opts)
}

func TestGenerate_FileSetAndModes(t *testing.T) {
	scripts := map[string]fs.FileMode{
		"scripts/bootstrap.sh":      0o755,
		"scripts/wait-bootstrap.sh": 0o755,
		"scripts/build-closure.sh":  0o755,
		"scripts/sanitize.sh":       0o755,
	}
	base := map[string]fs.FileMode{"image.pkr.hcl": 0o644, "README.md": 0o644, "metadata.json": 0o644}
	for k, v := range scripts {
		base[k] = v
	}

	t.Run("template only", func(t *testing.T) {
		opts := bundleOpts(t, selected(nil))
		generate(t, opts)
		assertFiles(t, bundleFiles(t, opts.OutputDir), base)
	})
	t.Run("rendered", func(t *testing.T) {
		opts := bundleOpts(t, selected(withPackages))
		generate(t, opts)
		want := map[string]fs.FileMode{"nix/flake.nix": 0o644}
		for k, v := range base {
			want[k] = v
		}
		assertFiles(t, bundleFiles(t, opts.OutputDir), want)
	})
}

func assertFiles(t *testing.T, got, want map[string]fs.FileMode) {
	t.Helper()
	names := func(m map[string]fs.FileMode) []string {
		var out []string
		for k, v := range m {
			out = append(out, k+" "+v.String())
		}
		sort.Strings(out)
		return out
	}
	if g, w := strings.Join(names(got), "\n"), strings.Join(names(want), "\n"); g != w {
		t.Fatalf("bundle files:\n%s\nwant:\n%s", g, w)
	}
}

func TestGenerate_HCL(t *testing.T) {
	opts := bundleOpts(t, selected(withPackages))
	generate(t, opts)
	hcl := readFile(t, filepath.Join(opts.OutputDir, "image.pkr.hcl"))

	for _, want := range []string{
		"required_plugins",
		`source  = "github.com/hashicorp/digitalocean"`,
		`version = "= ` + PackerPluginVersion + `"`,
		// The config's image is "tag:my-dev"; the build source is fixed.
		`image          = "ubuntu-24-04-x64"`,
		`ssh_username   = "root"`,
		`user_data_file = "scripts/bootstrap.sh"`,
		`post-processor "manifest"`,
		`output     = "packer-manifest.json"`,
		`default = "blr1"`,
		`default = "s-2vcpu-4gb"`,
		`default = "alice"`,
		`"scripts/wait-bootstrap.sh"`,
		`"scripts/build-closure.sh"`,
		`"scripts/sanitize.sh"`,
		`source      = "metadata.json"`,
		`source      = "nix"`,
		`destination = "/var/lib/bivouac-image"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("HCL missing %q\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "tag:my-dev") {
		t.Error("HCL carries the launch reference; the build source must be the stock mapping")
	}

	// The region is a variable, so its marker follows the variable: a
	// build run with -var region=... must not be tagged with the default.
	// Evaluated with the defaults, the list is exactly SnapshotTags.
	tags := SnapshotTags("my-dev", "x86_64", "${var.region}", "fish")
	quoted := make([]string, len(tags))
	for i, tag := range tags {
		quoted[i] = `"` + tag + `"`
	}
	if want := "snapshot_tags  = [" + strings.Join(quoted, ", ") + "]"; !strings.Contains(hcl, want) {
		t.Errorf("HCL missing %s\n%s", want, hcl)
	}

	// Packer runs the scripts in order, with the uploads between waiting
	// for the bootstrap and building the closure.
	order := []string{
		"scripts/wait-bootstrap.sh",
		`source      = "nix"`,
		`source      = "metadata.json"`,
		"scripts/build-closure.sh",
		"scripts/sanitize.sh",
	}
	last := -1
	for _, step := range order {
		i := strings.Index(hcl, step)
		if i <= last {
			t.Fatalf("%q out of order in\n%s", step, hcl)
		}
		last = i
	}
}

func TestGenerate_HCLWithoutRenderUploadsNoNix(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	generate(t, opts)
	if hcl := readFile(t, filepath.Join(opts.OutputDir, "image.pkr.hcl")); strings.Contains(hcl, `source      = "nix"`) {
		t.Fatalf("HCL uploads nix/ that was never rendered\n%s", hcl)
	}
}

func TestGenerate_TokenOnlyFromEnv(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	generate(t, opts)
	hcl := readFile(t, filepath.Join(opts.OutputDir, "image.pkr.hcl"))
	if n := strings.Count(hcl, `env("DIGITALOCEAN_TOKEN")`); n != 1 {
		t.Fatalf(`env("DIGITALOCEAN_TOKEN") appears %d times, want 1`, n)
	}
	if n := strings.Count(hcl, "api_token"); n != 1 || !strings.Contains(hcl, "api_token      = var.do_token") {
		t.Fatalf("api_token must come only from var.do_token\n%s", hcl)
	}
	if !strings.Contains(hcl, "sensitive = true") {
		t.Fatalf("token variable is not marked sensitive\n%s", hcl)
	}
}

func TestGenerate_Metadata(t *testing.T) {
	t.Run("template only", func(t *testing.T) {
		opts := bundleOpts(t, selected(nil))
		b := generate(t, opts)
		ref := "github:jskswamy/bivouac?dir=templates#python-x86_64-linux"
		sum := sha256.Sum256([]byte(ref))
		want := Metadata{
			FormatVersion: FormatVersion, BivouacVersion: "v0.0.0-test",
			SourceImage: StockSourceX86, Arch: "x86_64", Region: "blr1", Size: "s-2vcpu-4gb",
			Shell: "fish", Username: "alice", Tag: "my-dev",
			TemplateRef: ref, FlakeRef: ref, ConfigFingerprint: hex.EncodeToString(sum[:]),
		}
		if b.Metadata != want || b.Dir != opts.OutputDir {
			t.Fatalf("bundle = %+v\nwant metadata %+v", b, want)
		}
		var onDisk Metadata
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(opts.OutputDir, "metadata.json"))), &onDisk); err != nil {
			t.Fatal(err)
		}
		if onDisk != want {
			t.Fatalf("metadata.json = %+v, want %+v", onDisk, want)
		}
	})
	t.Run("rendered", func(t *testing.T) {
		opts := bundleOpts(t, selected(withPackages))
		b := generate(t, opts)
		flake := readFile(t, filepath.Join(opts.OutputDir, "nix", "flake.nix"))
		sum := sha256.Sum256([]byte("path:./nix#default" + flake))
		if b.Metadata.FlakeRef != "path:./nix#default" || b.Metadata.ConfigFingerprint != hex.EncodeToString(sum[:]) {
			t.Fatalf("metadata = %+v", b.Metadata)
		}
		if !strings.Contains(flake, `pkgs."ripgrep"`) {
			t.Fatalf("flake.nix is not the rendered config:\n%s", flake)
		}
	})
}

func TestGenerate_BootstrapIsTheSharedSoftwareSetup(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	generate(t, opts)
	got := readFile(t, filepath.Join(opts.OutputDir, "scripts", "bootstrap.sh"))
	if !strings.Contains(got, "install.determinate.systems/nix") || !strings.Contains(got, "/usr/bin/fish") {
		t.Fatalf("bootstrap.sh is not the rendered fish bootstrap:\n%s", got)
	}
	if strings.Contains(got, "useradd") {
		t.Fatal("bootstrap.sh creates an account; access belongs to the launch")
	}
}

func TestGenerate_RejectsUnsupportedInputs(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*BundleOptions)
		wants []string
	}{
		{"arm64", func(o *BundleOptions) { o.Selected = selected(func(c *config.Config) { c.Arch = "arm64" }) }, []string{"arm64", "x86_64"}},
		{"bad tag", func(o *BundleOptions) { o.Tag = "bivouac-x" }, []string{"bivouac-x"}},
		{"empty tag", func(o *BundleOptions) { o.Tag = "" }, []string{"tag"}},
		{"bad username", func(o *BundleOptions) { o.Username = "Bad User" }, []string{"Bad User"}},
		{"bad region", func(o *BundleOptions) {
			o.Selected = selected(func(c *config.Config) { r := `blr1" x`; c.Region = &r })
		}, []string{"region", `blr1\" x`}},
		{"bad size", func(o *BundleOptions) {
			o.Selected = selected(func(c *config.Config) { s := "S 2"; c.Size = &s })
		}, []string{"size", "S 2"}},
	}
	for _, local := range []string{"path:/src/tmpl#x", "/src/tmpl#x", "./tmpl#x", "../tmpl#x", "~/tmpl#x", "git+file:///src/tmpl#x", "file:///src/t.tar.gz#x"} {
		cases = append(cases, struct {
			name  string
			mut   func(*BundleOptions)
			wants []string
		}{"template " + local, func(o *BundleOptions) {
			o.Selected = selected(func(c *config.Config) { c.Template = &local })
		}, []string{"template", local}})
	}
	for _, local := range []string{"path:/src/flake", "./flake", "git+file:/src/flake", "~/flake"} {
		cases = append(cases, struct {
			name  string
			mut   func(*BundleOptions)
			wants []string
		}{"flake " + local, func(o *BundleOptions) {
			o.Selected = selected(func(c *config.Config) {
				c.Flakes = []config.Flake{{Url: "github:a/b"}, {Url: local, Packages: []string{"x"}}}
			})
		}, []string{"flakes[1].url", local}})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := bundleOpts(t, selected(nil))
			tc.mut(&opts)
			_, err := Generate(opts)
			if err == nil {
				t.Fatal("Generate succeeded")
			}
			for _, w := range tc.wants {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			if _, statErr := os.Stat(opts.OutputDir); !os.IsNotExist(statErr) {
				t.Errorf("output dir was created on a rejected input (stat: %v)", statErr)
			}
		})
	}
}

// TestGenerate_BundleIsSecretFreeAndSelfContained walks every file of a
// bundle generated with a token and a private key in the environment
// and asserts none of what a shared, committed bundle must not carry.
func TestGenerate_BundleIsSecretFreeAndSelfContained(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "dop_v1_supersecret")
	opts := bundleOpts(t, selected(func(c *config.Config) {
		withPackages(c)
		c.Flakes = []config.Flake{{Url: "github:a/b", Packages: []string{"x"}}}
		c.Settings = map[string]any{"programs.git.userName": "Alice"}
	}))
	home, _ := os.UserHomeDir()
	wd, _ := os.Getwd()
	forbidden := []string{
		"DIGITALOCEAN_TOKEN=", "dop_v1_supersecret", "PRIVATE KEY", "bivouac ",
		opts.OutputDir, filepath.Dir(opts.OutputDir), os.TempDir() + "/", wd, opts.Selected.Path,
		"/Users/", "/home/",
	}
	if home != "" {
		forbidden = append(forbidden, home)
	}
	generate(t, opts)
	for rel := range bundleFiles(t, opts.OutputDir) {
		content := readFile(t, filepath.Join(opts.OutputDir, rel))
		for _, f := range forbidden {
			if strings.Contains(content, f) {
				t.Errorf("%s contains %q", rel, f)
			}
		}
	}
}

func TestGenerate_README(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	generate(t, opts)
	readme := readFile(t, filepath.Join(opts.OutputDir, "README.md"))
	for _, want := range []string{"packer init .", "packer validate .", "packer build .", "DIGITALOCEAN_TOKEN", "arbitrary Nix", "my-dev"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README missing %q", want)
		}
	}
}

func TestGenerate_DefaultsUsernameToRemoteUser(t *testing.T) {
	opts := bundleOpts(t, selected(nil))
	opts.Username = ""
	b := generate(t, opts)
	if b.Metadata.Username == "" {
		t.Fatal("username not defaulted")
	}
}

// TestGenerate_PackerFmt checks the HCL against the real Packer when one
// is installed. A fake would only prove the fake agrees with itself.
func TestGenerate_PackerFmt(t *testing.T) {
	packer, err := exec.LookPath("packer")
	if err != nil {
		t.Skip("packer not installed")
	}
	opts := bundleOpts(t, selected(withPackages))
	generate(t, opts)
	cmd := exec.Command(packer, "fmt", "-check", "-diff", ".")
	cmd.Dir = opts.OutputDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("packer fmt -check: %v\n%s", err, out)
	}
}
