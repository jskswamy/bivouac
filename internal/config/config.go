package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/apple/pkl-go/pkl"

	"github.com/jskswamy/bivouac/internal/tool"
)

// Resolve loads the project's bivouac.pkl at path, merges it with a
// personal base config if one is found (see resolveBasePath), and
// returns the merged Config. Scalar fields take the project's value if
// set, else the base's; list fields are additive (base's entries
// first, then the project's). Returns an error naming any of
// region/size/template still unset after merging.
//
// Named Resolve, not Load, because the generated Config.pkl.go already
// defines a lower-level Load(ctx, evaluator, source) — this function
// builds on that instead of the generated LoadFromPath, so it can point
// the evaluator's package cache at pklCacheDir() rather than pkl-go's
// hardcoded ~/.pkl/cache default (LoadFromPath exposes no override hook
// for this).
func Resolve(ctx context.Context, path string) (Resolved, error) {
	if _, err := tool.Require("pkl"); err != nil {
		return Resolved{}, err
	}

	l, err := loadLayered(ctx, path)
	if err != nil {
		return Resolved{}, err
	}

	projectKeys, projectKeyErr := configSSHKeyFiles(path, l.project)
	keyFiles := projectKeys
	var baseKeyErr error
	if l.base != nil {
		baseKeys, keyErr := configSSHKeyFiles(l.basePath, *l.base)
		baseKeyErr = keyErr
		keyFiles = append(baseKeys, projectKeys...)
	}

	if err := errors.Join(baseKeyErr, projectKeyErr); err != nil {
		return Resolved{}, err
	}
	if err := validate(l.merged); err != nil {
		return Resolved{}, err
	}
	result := resolved(l.merged)
	result.SSHKeyFiles = keyFiles
	return result, nil
}

// layered is a project file merged over its personal base. base is nil
// when there was nothing to merge: no base file, or the project file is
// the base file itself.
type layered struct {
	project  Config
	basePath string
	base     *Config
	merged   Config
}

// loadLayered is the load-and-merge half of Resolve, shared with
// LoadSelected so the two cannot drift apart on how a base is found.
//
// A file that resolves to its own base is loaded once and not merged:
// merging it with itself would double every additive list. Resolve's
// callers always pass a project bivouac.pkl, so this only changes
// anything for LoadSelected, which may be handed base.pkl directly.
func loadLayered(ctx context.Context, path string) (layered, error) {
	project, projectDeclared, err := loadResolved(ctx, path)
	if err != nil {
		return layered{}, err
	}
	l := layered{project: project, merged: project}

	basePath, err := resolveBasePath(path, project.BasePath)
	if err != nil {
		return layered{}, fmt.Errorf("resolving base config path: %w", err)
	}
	l.basePath = basePath
	if sameFile(path, basePath) {
		return l, nil
	}
	if _, statErr := os.Stat(basePath); statErr == nil {
		base, _, err := loadResolved(ctx, basePath)
		if err != nil {
			return layered{}, err
		}
		l.base = &base
		l.merged = mergeConfig(base, project, projectDeclared)
	} else if !os.IsNotExist(statErr) {
		return layered{}, fmt.Errorf("checking base config %s: %w", basePath, statErr)
	}
	return l, nil
}

// SelectionKind says which of LoadSelected's candidates was chosen.
type SelectionKind string

const (
	SelectionExplicit SelectionKind = "explicit"
	SelectionProject  SelectionKind = "project"
	SelectionBase     SelectionKind = "base"
)

// Selected is the one config file an image operation works from.
type Selected struct {
	Path   string
	Kind   SelectionKind
	Config Resolved
}

// LoadSelected picks exactly one config file for an image operation and
// loads it: explicit if non-empty, else projectDir/bivouac.pkl, else
// the personal base.pkl. projectDir may be "" when there is no repo.
//
// Selection is by presence, never by validity. A file that exists but
// fails to load is the answer, not a reason to try the next one: falling
// through from a broken project config to base would quietly build an
// image from settings the user did not mean, and an explicit path the
// user named must never be second-guessed at all.
//
// An explicit or project file keeps its normal base inheritance. The
// base selected on its own (or named explicitly) is loaded exactly once
// rather than merged with itself; see loadLayered.
//
// SSH keys are neither resolved nor required, so Config.SSHKeyFiles is
// always empty: Packer builds with temporary keys, and a machine with
// no personal key must still be able to build or generate. Region, size
// and template are still validated, because the build needs all three.
//
// Needs only the pkl CLI. Generating a bundle from the result must not
// require Packer, a cloud token or Nix, so nothing here touches them.
func LoadSelected(ctx context.Context, explicit, projectDir string) (Selected, error) {
	sel, err := selectConfig(explicit, projectDir)
	if err != nil {
		return Selected{}, err
	}
	if _, err := tool.Require("pkl"); err != nil {
		return Selected{}, err
	}
	l, err := loadLayered(ctx, sel.Path)
	if err != nil {
		return Selected{}, err
	}
	if err := validate(l.merged); err != nil {
		return Selected{}, fmt.Errorf("%s: %w", sel.Path, err)
	}
	sel.Config = resolved(l.merged)
	return sel, nil
}

// selectConfig applies LoadSelected's order by file presence alone.
func selectConfig(explicit, projectDir string) (Selected, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return Selected{}, fmt.Errorf("config file %s: %w", explicit, err)
		}
		return Selected{Path: explicit, Kind: SelectionExplicit}, nil
	}

	projectLoc := "no project directory"
	if projectDir != "" {
		project := filepath.Join(projectDir, "bivouac.pkl")
		projectLoc = project
		found, err := exists(project)
		if err != nil {
			return Selected{}, err
		}
		if found {
			return Selected{Path: project, Kind: SelectionProject}, nil
		}
	}

	base, err := DefaultBasePath()
	if err != nil {
		return Selected{}, fmt.Errorf("resolving base config path: %w", err)
	}
	found, err := exists(base)
	if err != nil {
		return Selected{}, err
	}
	if found {
		return Selected{Path: base, Kind: SelectionBase}, nil
	}
	return Selected{}, fmt.Errorf("no config found: pass --config <file>, or create %s or %s", projectLoc, base)
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("checking config %s: %w", path, err)
}

// ResolveBase loads the personal base config on its own, for the one
// caller that has no project file to anchor on.
//
// Returns the zero Config when there is no base file -- a machine
// without one is the ordinary case, not an error.
//
// Config, not Resolved, and deliberately unvalidated: a base config is
// half of an instance spec by design, so requiring region/size/template
// of it would reject exactly the personal-defaults files this exists to
// read. Callers get what the file declares and nothing more, which is
// why this is only safe for fields that mean something on their own --
// herdrTabs is the one, and anything that decides how an instance is
// built must keep going through Resolve.
//
// Only the default path is consulted. An override lives in basePath on
// a project file, and with no project file there is nothing to read one
// from.
func ResolveBase(ctx context.Context) (Config, error) {
	basePath, err := DefaultBasePath()
	if err != nil {
		return Config{}, err
	}
	if _, err := os.Stat(basePath); err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("checking base config %s: %w", basePath, err)
	}
	if _, err := tool.Require("pkl"); err != nil {
		return Config{}, err
	}
	base, _, err := loadResolved(ctx, basePath)
	return base, err
}

// loadResolved reads path's raw content, injects bivouac's own
// embedded schema as its amends target (path itself must not declare
// one), and evaluates the result with an evaluator pointed at
// pklCacheDir() for its package cache.
//
// It also returns the set of fields the file declares, from the same
// bytes it evaluated: the loaded Config fills schema defaults in, so it
// alone cannot tell mergeConfig whether the file chose a value.
func loadResolved(ctx context.Context, path string) (ret Config, declared map[string]struct{}, err error) {
	// #nosec G304 -- path is the intended bivouac.pkl/base.pkl to load;
	// reading a caller-supplied config path is this function's entire
	// job (see docs/config.md's "A note on trust" -- such a file is
	// already treated as trusted, script-equivalent input).
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("loading %s: %w", path, err)
	}

	tmpPath, cleanup, err := injectSchema(path, raw)
	if err != nil {
		return Config{}, nil, err
	}
	defer cleanup()

	cacheDir, err := pklCacheDir()
	if err != nil {
		return Config{}, nil, fmt.Errorf("resolving pkl cache directory: %w", err)
	}

	pklPath, lookErr := exec.LookPath("pkl")
	if lookErr != nil {
		return Config{}, nil, fmt.Errorf("pkl CLI not found on PATH (run inside `nix develop`, or install it: https://pkl-lang.org/main/current/pkl-cli/index.html#installation): %w", lookErr)
	}

	var pklCmd []string
	logPath, logErr := pklLogPath()
	if logErr == nil {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err == nil {
			shPath, sErr := exec.LookPath("sh")
			if sErr != nil {
				shPath = "/bin/sh"
			}
			if _, statErr := os.Stat(shPath); statErr == nil {
				pklCmd = []string{shPath, "-c", `exec "$1" "${@:3}" 2>> "$2"`, "sh", pklPath, logPath}
			}
		}
	}

	evaluator, err := pkl.NewEvaluatorWithCommand(ctx, pklCmd, pkl.PreconfiguredOptions, func(opts *pkl.EvaluatorOptions) {
		opts.CacheDir = cacheDir
	})
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Config{}, nil, fmt.Errorf("pkl CLI not found on PATH (run inside `nix develop`, or install it: https://pkl-lang.org/main/current/pkl-cli/index.html#installation): %w", err)
		}
		return Config{}, nil, fmt.Errorf("loading %s: %w", path, err)
	}
	defer func() {
		cerr := evaluator.Close()
		if err == nil {
			err = cerr
		}
	}()

	ret, err = Load(ctx, evaluator, pkl.FileSource(tmpPath))
	if err != nil {
		return Config{}, nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return ret, declaredFields(raw), nil
}

// mergeConfig layers project over base: scalars use project's value if
// set, else base's; lists are additive, base's entries first.
//
// Arch, image, tailscale and beads have schema defaults, so a project
// that never mentions them still loads with a value and "unset" cannot
// be seen in it. For those four, projectDeclared decides instead:
// without it, `image = "tag:my-dev"` in a base would be silently
// replaced by every project's default, and a project writing the
// default out loud could not override a base that says otherwise.
//
// BasePath is intentionally not carried into the returned Config — it's
// only meaningful for deciding which base file to merge, not as part of
// the merged output.
func mergeConfig(base, project Config, projectDeclared map[string]struct{}) Config {
	declared := func(field string) bool {
		_, ok := projectDeclared[field]
		return ok
	}
	return Config{
		Provider:            pick(declared(FieldProvider), project.Provider, base.Provider),
		Digitalocean:        mergeDigitalOcean(base.Digitalocean, project.Digitalocean),
		Gcloud:              mergeGCloud(base.Gcloud, project.Gcloud),
		Region:              coalesce(project.Region, base.Region),
		Size:                coalesce(project.Size, base.Size),
		Template:            coalesce(project.Template, base.Template),
		Arch:                pick(declared(FieldArch), project.Arch, base.Arch),
		Image:               pick(declared(FieldImage), project.Image, base.Image),
		Tailscale:           pick(declared(FieldTailscale), project.Tailscale, base.Tailscale),
		Shell:               coalesce(project.Shell, base.Shell),
		Beads:               pick(declared(FieldBeads), project.Beads, base.Beads),
		SshKeys:             mergeStringSlicePtrs(base.SshKeys, project.SshKeys),
		Packages:            append(append([]string{}, base.Packages...), project.Packages...),
		Agents:              append(append([]string{}, base.Agents...), project.Agents...),
		AllowUnfreePackages: append(append([]string{}, base.AllowUnfreePackages...), project.AllowUnfreePackages...),
		Flakes:              append(append([]Flake{}, base.Flakes...), project.Flakes...),
		HerdrTabs:           append(append([]HerdrTab{}, base.HerdrTabs...), project.HerdrTabs...),
		Settings:            mergeSettings(base.Settings, project.Settings),
	}
}

func mergeDigitalOcean(base, project *DigitalOceanConfig) *DigitalOceanConfig {
	if base == nil && project == nil {
		return nil
	}
	if base == nil {
		return project
	}
	if project == nil {
		return base
	}
	return &DigitalOceanConfig{
		Region: coalesce(project.Region, base.Region),
		Size:   coalesce(project.Size, base.Size),
		Image:  coalesce(project.Image, base.Image),
	}
}

func mergeGCloud(base, project *GCloudConfig) *GCloudConfig {
	if base == nil && project == nil {
		return nil
	}
	if base == nil {
		return project
	}
	if project == nil {
		return base
	}
	res := *project
	if res.Project == "" {
		res.Project = base.Project
	}
	if res.Zone == "" {
		res.Zone = base.Zone
	}
	if res.MachineType == "" {
		res.MachineType = base.MachineType
	}
	if res.Network == "" || res.Network == "default" {
		if base.Network != "" {
			res.Network = base.Network
		}
	}
	if res.Subnetwork == nil {
		res.Subnetwork = base.Subnetwork
	}
	if !res.ExternalIP && base.ExternalIP {
		res.ExternalIP = base.ExternalIP
	}
	return &res
}

func pick[T any](useProject bool, project, base T) T {
	if useProject {
		return project
	}
	return base
}

func coalesce(project, base *string) *string {
	if project != nil {
		return project
	}
	return base
}

func mergeStringSlicePtrs(base, project *[]string) *[]string {
	if base == nil && project == nil {
		return nil
	}
	merged := []string{}
	if base != nil {
		merged = append(merged, *base...)
	}
	if project != nil {
		merged = append(merged, *project...)
	}
	return &merged
}

// mergeSettings layers project over base by key: the project's value
// replaces any matching base key and adds any new one. Unlike every
// Listing field above, this is not additive -- a settings value is a
// single override, not an accumulating list, so keeping both a base and
// a project value for the same key would mean picking one arbitrarily
// at render time instead of deciding it here, once.
func mergeSettings(base, project map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(project))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range project {
		merged[k] = v
	}
	return merged
}

func validate(c Config) error {
	var missing []string
	if c.Template == nil || *c.Template == "" {
		missing = append(missing, "template")
	}

	provider := c.Provider
	if provider == "" {
		provider = "digitalocean"
	}

	switch provider {
	case "digitalocean":
		regionSet := (c.Region != nil && *c.Region != "") ||
			(c.Digitalocean != nil && c.Digitalocean.Region != nil && *c.Digitalocean.Region != "")
		if !regionSet {
			missing = append(missing, "region")
		}
		sizeSet := (c.Size != nil && *c.Size != "") ||
			(c.Digitalocean != nil && c.Digitalocean.Size != nil && *c.Digitalocean.Size != "")
		if !sizeSet {
			missing = append(missing, "size")
		}
	case "gcloud":
		if c.Gcloud == nil {
			missing = append(missing, "gcloud (configuration block required when provider is gcloud)")
		} else {
			if c.Gcloud.Project == "" {
				missing = append(missing, "gcloud.project")
			}
			if c.Gcloud.Zone == "" {
				missing = append(missing, "gcloud.zone")
			}
			if c.Gcloud.MachineType == "" {
				missing = append(missing, "gcloud.machineType")
			}
		}
	default:
		return fmt.Errorf("unsupported provider %q (expected digitalocean or gcloud)", provider)
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s) after merging project and base config: %s", strings.Join(missing, ", "))
	}
	return nil
}
