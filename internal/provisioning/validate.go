package provisioning

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jskswamy/bivouac/internal/config"
)

// Runner runs one nix invocation and returns its combined output.
//
// An interface because the nix that matters is the instance's, not the
// user's. bivouac requires no local nix -- every build happens on the
// VM -- and checking a config against whatever nix a laptop happens to
// have would prove the wrong thing anyway: a different nixpkgs, a
// different system, a different answer.
type Runner interface {
	RunNix(ctx context.Context, args ...string) ([]byte, error)
}

// LocalRunner runs nix on the machine bivouac itself is running on.
//
// Not used by any command. It exists for CI and for the tests that need
// a real evaluator, so that requiring nix stays a choice the caller
// makes rather than something a user of bivouac inherits.
type LocalRunner struct{}

// RunNix implements Runner.
func (LocalRunner) RunNix(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("nix"); err != nil {
		return nil, fmt.Errorf("nix CLI not found on PATH (run inside `nix develop`, or install it: https://nixos.org/download): %w", err)
	}
	// #nosec G204 -- argv-array exec.Command, no shell; args are built
	// from bivouac.pkl, already treated as trusted input (see
	// docs/config.md's "A note on trust").
	return exec.CommandContext(ctx, "nix", args...).CombinedOutput()
}

// Validate checks that everything cfg names resolves to something real
// -- its template, its `packages`, its agents' packages and its
// flakes[] -- without building or switching anything.
//
// Returns an error naming every problem found, not just the first: a
// config with three bad package names should take one round of fixing,
// not three.
func Validate(ctx context.Context, r Runner, cfg config.Resolved) error {
	var problems []string
	system := NixSystem(cfg.Arch)

	// No "is the template set" branch: config.Resolved is what Resolve
	// returns once it has proved that, so the rule is stated once, in
	// config.validate, rather than restated here in different words.
	templateRef := ResolveTemplateRef(cfg.Template, cfg.Arch)
	url, name := splitFlakeRef(templateRef)
	attr := "homeConfigurations." + name
	if NeedsRender(cfg.Config) {
		attr = "homeManagerModules." + templateModuleName(name, cfg.Arch)
	}
	if err := evalExists(ctx, r, url, attr); err != nil {
		problems = append(problems, fmt.Sprintf("template %q: %v", cfg.Template, err))
	}

	// packages are nixpkgs attribute names, which nothing before this
	// point checks: Pkl types them as plain strings, so a typo travels
	// all the way into the rendered flake and surfaces as a Nix
	// evaluation error part-way through a switch.
	for _, pkg := range cfg.Packages {
		if err := evalExists(ctx, r, NixpkgsRef, "legacyPackages."+system+"."+pkg); err != nil {
			problems = append(problems, fmt.Sprintf("package %q: not in nixpkgs (%v)", pkg, err))
		}
	}

	agentPkgs, _, err := resolveAgents(cfg.Agents)
	if err != nil {
		problems = append(problems, err.Error())
	}
	// Checked although `agents` is a closed set: the set is closed
	// against typos, not against nixpkgs renaming or dropping one of
	// the packages it maps to.
	for _, pkg := range agentPkgs {
		if err := evalExists(ctx, r, NixpkgsRef, "legacyPackages."+system+"."+pkg); err != nil {
			problems = append(problems, fmt.Sprintf("agent package %q: not in nixpkgs (%v)", pkg, err))
		}
	}

	for _, f := range cfg.Flakes {
		for _, pkg := range f.Packages {
			if err := evalExists(ctx, r, f.Url, "packages."+system+"."+pkg); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q package %q: %v", f.Url, pkg, err))
			}
		}
		for _, m := range f.Modules {
			if err := evalExists(ctx, r, f.Url, "homeManagerModules."+m); err != nil {
				problems = append(problems, fmt.Sprintf("flake %q module %q: %v", f.Url, m, err))
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
// the attribute path far enough to bind it -- proving existence
// without trying to print a value that might not be representable
// (a home-manager module, a function), and without forcing a
// derivation whose licence check would refuse an unfree package that
// is nonetheless there.
func evalExists(ctx context.Context, r Runner, ref, attr string) error {
	// --impure: binding a homeConfigurations.<name> attribute already
	// forces home-manager to type-check its full option set, including
	// common.nix's home.username/homeDirectory, which read $USER/$HOME
	// via builtins.getEnv (see internal/reconcile/reconcile.go for the
	// same flag on the real switch invocation). Harmless for the
	// homeManagerModules/packages attrs validated elsewhere in this
	// file, which don't touch that impure builtin.
	out, err := r.RunNix(ctx, "eval", "--impure", ref+"#"+attr, "--apply", `x: "ok"`)
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
