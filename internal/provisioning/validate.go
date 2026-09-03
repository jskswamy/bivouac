package provisioning

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jskswamy/cloudlab/internal/config"
)

// Validate checks that cfg's template and flakes[] resolve to real,
// evaluable flake outputs, without building or switching anything.
// Returns an error naming every problem found, not just the first.
func Validate(ctx context.Context, cfg config.Config) error {
	if _, err := exec.LookPath("nix"); err != nil {
		return fmt.Errorf("nix CLI not found on PATH (run inside `nix develop`, or install it: https://nixos.org/download): %w", err)
	}

	var problems []string

	if cfg.Template == nil {
		problems = append(problems, "template: not set")
	} else {
		templateRef := ResolveTemplateRef(*cfg.Template, cfg.Arch)
		url, name := splitFlakeRef(templateRef)
		attr := "homeConfigurations." + name
		if NeedsRender(cfg) {
			attr = "homeManagerModules." + templateModuleName(name, cfg.Arch)
		}
		if err := evalExists(ctx, url, attr); err != nil {
			problems = append(problems, fmt.Sprintf("template %q: %v", *cfg.Template, err))
		}
	}

	system := NixSystem(cfg.Arch)
	for _, f := range cfg.Flakes {
		for _, pkg := range f.Packages {
			if err := evalExists(ctx, f.Url, "packages."+system+"."+pkg); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q package %q: %v", f.Url, pkg, err))
			}
		}
		if f.Modules {
			if err := evalExists(ctx, f.Url, "homeManagerModules.default"); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q module: %v", f.Url, err))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("provisioning validation failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// evalExists reports whether ref#attr resolves to anything at all.
// The --apply lambda never touches x, so Nix only needs to resolve
// the attribute path far enough to bind it — proving existence
// without trying to print a value that might not be representable
// (a home-manager module, a function).
func evalExists(ctx context.Context, ref, attr string) error {
	// #nosec G204 -- argv-array exec.Command, no shell; ref/attr come
	// from cloudlab.pkl, already treated as trusted input (see
	// docs/config.md's "A note on trust").
	out, err := exec.CommandContext(ctx, "nix", "eval", ref+"#"+attr, "--apply", `x: "ok"`).CombinedOutput()
	if err != nil {
		if reason := lastNixError(string(out)); reason != "" {
			return fmt.Errorf("%s", reason)
		}
		return err
	}
	return nil
}

// lastNixError extracts Nix's own final, specific error line from its
// often trace-heavy output, so validation errors stay short and
// actionable instead of dumping a full Nix stack trace at the user.
func lastNixError(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "error:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "error:"))
		}
	}
	return strings.TrimSpace(output)
}
