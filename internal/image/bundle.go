package image

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/identity"
	"github.com/jskswamy/bivouac/internal/provisioning"
)

// PackerPluginVersion pins the HashiCorp DigitalOcean plugin the bundle
// declares, so `packer init` on a CI runner a year from now installs the
// plugin this template was written against rather than whatever is newest.
// v1.4.1, tagged 2024-11-07: v1.4.2 was tagged on 2026-10-15, too recent
// to have been exercised by anyone yet.
const PackerPluginVersion = "1.4.1"

// The fixed bundle layout. The builder scripts locate each other and the
// uploads by these names, so they are not free to change on one side.
const (
	bundleHCL      = "image.pkr.hcl"
	bundleREADME   = "README.md"
	bundleMetadata = "metadata.json"
	bundleScripts  = "scripts"
	bundleFlake    = "nix/flake.nix"
	renderedFlake  = "path:./nix#default"
	supportedArch  = "x86_64"
)

//go:embed bundle
var bundleFS embed.FS

// "[[" rather than "{{", because the HCL carries Packer's own
// {{ .Path }} templates through to Packer untouched.
var (
	hclTemplate    = mustBundleTemplate("bundle/image.pkr.hcl.tmpl")
	readmeTemplate = mustBundleTemplate("bundle/README.md.tmpl")
)

func mustBundleTemplate(name string) *template.Template {
	return template.Must(template.New(filepath.Base(name)).Delims("[[", "]]").ParseFS(bundleFS, name))
}

// builderScripts are copied verbatim; bootstrap.sh is rendered instead,
// from the same template a stock launch runs.
var builderScripts = []string{"wait-bootstrap.sh", "build-closure.sh", "sanitize.sh"}

// BundleOptions says what to generate and where.
type BundleOptions struct {
	OutputDir      string
	Selected       config.Selected
	Tag            string
	Username       string // default identity.RemoteUser(); validated with identity.ValidRemoteUser
	BivouacVersion string
}

// Metadata is the nonsecret record of what a bundle builds. It goes into
// the bundle and, through the build, onto the image, for diagnosing an
// image later; launch decisions rest on the snapshot's tags, not on this.
type Metadata struct {
	FormatVersion     int    `json:"format_version"`
	BivouacVersion    string `json:"bivouac_version"`
	SourceImage       string `json:"source_image"`
	Arch              string `json:"arch"`
	Region            string `json:"region"`
	Size              string `json:"size"`
	Shell             string `json:"shell"`
	Username          string `json:"username"`
	Tag               string `json:"tag"`
	TemplateRef       string `json:"template_ref"`
	FlakeRef          string `json:"flake_ref"`
	ConfigFingerprint string `json:"config_fingerprint"`
}

// Bundle is a generated bundle directory and what it builds.
type Bundle struct {
	Dir      string
	Metadata Metadata
}

// Generate writes a self-contained Packer bundle for opts.Selected into
// opts.OutputDir: one a user can commit and run with plain Packer, with
// no Bivouac, local Nix, original config file or credential behind it.
//
// Everything is validated and rendered before anything is written, so a
// rejected input leaves no half-made directory behind, and an output
// directory that already holds anything is refused rather than mixed
// into: a stale script from an earlier bundle would run in this build.
//
// It touches no network, Packer or Nix; Render and the bootstrap are
// pure, and the template is resolved by name, not evaluated.
func Generate(opts BundleOptions) (Bundle, error) {
	cfg := opts.Selected.Config
	files, meta, err := renderBundle(opts, cfg)
	if err != nil {
		return Bundle{}, err
	}
	if err := prepareOutputDir(opts.OutputDir); err != nil {
		return Bundle{}, err
	}
	for _, f := range files {
		path := filepath.Join(opts.OutputDir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- a bundle meant to be read and committed
			return Bundle{}, err
		}
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			return Bundle{}, err
		}
		// WriteFile's mode is filtered by the umask; the layout's modes
		// are part of what the bundle promises.
		if err := os.Chmod(path, f.mode); err != nil {
			return Bundle{}, err
		}
	}
	return Bundle{Dir: opts.OutputDir, Metadata: meta}, nil
}

type bundleFile struct {
	path string
	data []byte
	mode os.FileMode
}

func renderBundle(opts BundleOptions, cfg config.Resolved) ([]bundleFile, Metadata, error) {
	arch := cfg.Arch
	if arch == "" {
		arch = supportedArch
	}
	// The schema accepts arm64, but the only build source mapped and
	// tested is DigitalOcean's x86_64 Ubuntu; guessing an ARM source
	// would produce an image nobody has checked boots.
	if arch != supportedArch {
		return nil, Metadata{}, fmt.Errorf("arch %q is not supported for image builds: only %s is (built from %s)", arch, supportedArch, StockSourceX86)
	}
	if err := ValidUserTag(opts.Tag); err != nil {
		return nil, Metadata{}, err
	}
	username := opts.Username
	if username == "" {
		u, err := identity.RemoteUser()
		if err != nil {
			return nil, Metadata{}, fmt.Errorf("deriving the remote username: %w", err)
		}
		username = u
	}
	// Interpolated into the builder's root-run commands.
	if !identity.ValidRemoteUser(username) {
		return nil, Metadata{}, fmt.Errorf("invalid remote username %q", username)
	}
	// Both are written into HCL strings; a slug cannot carry a quote or
	// a ${ that would change what the template means.
	for _, f := range []struct{ name, value string }{{"region", cfg.Region}, {"size", cfg.Size}} {
		if !slug.MatchString(f.value) {
			return nil, Metadata{}, fmt.Errorf("%s %q is not a DigitalOcean slug", f.name, f.value)
		}
	}

	templateRef := provisioning.ResolveTemplateRef(cfg.Template, arch)
	if err := remoteFlake("template", templateRef); err != nil {
		return nil, Metadata{}, err
	}
	for i, f := range cfg.Flakes {
		if err := remoteFlake(fmt.Sprintf("flakes[%d].url", i), f.Url); err != nil {
			return nil, Metadata{}, err
		}
	}

	bootstrap, err := provisioning.RenderBootstrap(cfg.Shell)
	if err != nil {
		return nil, Metadata{}, err
	}
	flakeRef, flake := templateRef, ""
	rendered := provisioning.NeedsRender(cfg.Config)
	if rendered {
		if flake, err = provisioning.Render(cfg.Config, templateRef); err != nil {
			return nil, Metadata{}, fmt.Errorf("rendering the Nix configuration: %w", err)
		}
		flakeRef = renderedFlake
	}
	sum := sha256.Sum256([]byte(flakeRef + flake))

	meta := Metadata{
		FormatVersion:     FormatVersion,
		BivouacVersion:    opts.BivouacVersion,
		SourceImage:       StockSourceX86,
		Arch:              arch,
		Region:            cfg.Region,
		Size:              cfg.Size,
		Shell:             cfg.Shell,
		Username:          username,
		Tag:               opts.Tag,
		TemplateRef:       templateRef,
		FlakeRef:          flakeRef,
		ConfigFingerprint: hex.EncodeToString(sum[:]),
	}

	data := struct {
		Metadata
		PluginVersion, SnapshotTags string
		Rendered                    bool
	}{meta, PackerPluginVersion, hclList(SnapshotTags(opts.Tag, arch, "${var.region}", cfg.Shell)), rendered}
	hcl, err := execute(hclTemplate, data)
	if err != nil {
		return nil, Metadata{}, err
	}
	readme, err := execute(readmeTemplate, data)
	if err != nil {
		return nil, Metadata{}, err
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, Metadata{}, err
	}

	files := []bundleFile{
		{bundleHCL, hcl, 0o644},
		{bundleREADME, readme, 0o644},
		{bundleMetadata, append(metaJSON, '\n'), 0o644},
		{bundleScripts + "/bootstrap.sh", []byte(bootstrap), 0o755},
	}
	for _, name := range builderScripts {
		b, err := bundleFS.ReadFile("bundle/scripts/" + name)
		if err != nil {
			return nil, Metadata{}, err
		}
		files = append(files, bundleFile{bundleScripts + "/" + name, b, 0o755})
	}
	if rendered {
		files = append(files, bundleFile{bundleFlake, []byte(flake), 0o644})
	}
	return files, meta, nil
}

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// remoteFlake rejects a flake reference that names the local disk. The
// builder is another machine: a local path would be dangling there, and
// copying the tree into the bundle would ship whatever else it holds.
// The user pushes it somewhere fetchable instead.
func remoteFlake(field, ref string) error {
	url, _, _ := strings.Cut(ref, "#")
	scheme, _, hasScheme := strings.Cut(url, ":")
	local := strings.HasPrefix(url, "/") || strings.HasPrefix(url, ".") || strings.HasPrefix(url, "~")
	if hasScheme && (scheme == "path" || scheme == "file" || strings.HasSuffix(scheme, "+file")) {
		local = true
	}
	if local {
		return fmt.Errorf("%s %q is a local path, which an image build cannot use: the builder is a remote machine; push it to a git host and reference that instead", field, ref)
	}
	return nil
}

func hclList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func execute(t *template.Template, data any) ([]byte, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// prepareOutputDir creates dir, or accepts it if it exists and is empty.
func prepareOutputDir(dir string) error {
	if dir == "" {
		return errors.New("no output directory")
	}
	f, err := os.Open(dir) // #nosec G304 -- the user's own --output
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dir, 0o755) // #nosec G301 -- a bundle meant to be read and committed
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Readdirnames(1); err == nil {
		return fmt.Errorf("output directory %s is not empty; choose another or remove it", dir)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("output directory %s: %w", dir, err)
	}
	return nil
}
