package provisioning

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/jskswamy/cloudlab/internal/config"
)

// NeedsRender reports whether cfg requires a per-instance wrapper
// flake, rather than using the template ref as-is. See the Provisioning
// design spec's render-trigger rule.
//
// Tailscale counts alongside packages/flakes because the rendered flake
// is the only place cloudlab.tailscale is ever set (see renderTmpl);
// the shared template defaults it to false. Left out of this condition,
// "tailscale = true" in a config with no packages and no flakes renders
// nothing, so the template's default stands and the tailscaled unit is
// never installed -- the flag silently does nothing at all.
func NeedsRender(cfg config.Config) bool {
	return len(cfg.Packages) > 0 || len(cfg.Flakes) > 0 || cfg.Tailscale || len(cfg.Agents) > 0
}

var renderTmpl = template.Must(template.New("flake").Parse(`{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    home-manager.url = "github:nix-community/home-manager";
    template.url = "{{.TemplateURL}}";
{{range $i, $f := .Flakes}}    flake{{$i}}.url = "{{$f.Url}}";
{{end}}  };
  outputs = { self, nixpkgs, home-manager, template{{range $i, $f := .Flakes}}, flake{{$i}}{{end}}, ... }: {
    homeConfigurations.default = home-manager.lib.homeManagerConfiguration {
      pkgs = import nixpkgs {
        system = "{{.System}}";
        config.allowUnfreePredicate = pkg: builtins.elem (nixpkgs.lib.getName pkg) [ {{range .UnfreePackages}}"{{.}}" {{end}}];
      };
      modules = [
        template.homeManagerModules."{{.TemplateName}}"
        { cloudlab.tailscale = {{.Tailscale}}; }
{{if .Packages}}        ({ pkgs, ... }: { home.packages = [ {{range .Packages}}pkgs."{{.}}" {{end}}]; })
{{end}}{{if .AgentPackages}}        ({ pkgs, ... }: { home.packages = [ {{range .AgentPackages}}pkgs."{{.}}" {{end}}]; })
{{end}}{{range $i, $f := .Flakes}}{{if $f.Packages}}        { home.packages = [ {{range $f.Packages}}flake{{$i}}.packages."{{$.System}}"."{{.}}" {{end}}]; }
{{end}}{{if $f.Modules}}        flake{{$i}}.homeManagerModules.default
{{end}}{{end}}      ];
    };
  };
}
`))

type renderData struct {
	TemplateURL  string
	TemplateName string
	System       string
	Tailscale    bool
	Packages     []string
	// AgentPackages are the nixpkgs attribute names cfg.Agents maps to,
	// and UnfreePackages the subset of those whose licence is unfree --
	// listed explicitly so the generated flake permits exactly the unfree
	// packages that were asked for, rather than switching allowUnfree on
	// wholesale and quietly permitting anything in Packages too.
	AgentPackages  []string
	UnfreePackages []string
	Flakes         []config.Flake
}

// agentPackages maps a config `agents` entry to its nixpkgs attribute
// name. The indirection exists because the attribute is not guessable
// from the tool's name: pi ships as pi-coding-agent, claude as
// claude-code. Keep in step with Config.pkl's agents union, which is
// what stops an unknown name reaching here in the first place.
var agentPackages = map[string]string{
	"claude":   "claude-code",
	"codex":    "codex",
	"copilot":  "github-copilot-cli",
	"cursor":   "cursor-cli",
	"opencode": "opencode",
	"pi":       "pi-coding-agent",
}

// unfreeAgentPackages lists the agentPackages values nixpkgs marks
// unfree, which it refuses to build unless explicitly permitted. Keyed
// by the same string lib.getName returns for that package, since that
// is what the generated flake's allowUnfreePredicate compares against.
var unfreeAgentPackages = map[string]bool{
	"claude-code":        true,
	"cursor-cli":         true,
	"github-copilot-cli": true,
}

// knownAgents lists the accepted agent names for error messages, read
// from agentPackages so it cannot drift from what is actually accepted.
func knownAgents() string {
	names := make([]string, 0, len(agentPackages))
	for name := range agentPackages {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveAgents maps agents to their nixpkgs attribute names, and
// returns the unfree subset alongside. An unknown name is an error
// rather than a silent omission: Pkl's union type already rejects one,
// so reaching here means the schema and this map have drifted apart,
// and dropping it would install nothing while reporting success.
func resolveAgents(agents []string) (pkgs, unfree []string, err error) {
	for _, a := range agents {
		name, ok := agentPackages[a]
		if !ok {
			return nil, nil, fmt.Errorf("unknown agent %q (known: %s)", a, knownAgents())
		}
		pkgs = append(pkgs, name)
		if unfreeAgentPackages[name] {
			unfree = append(unfree, name)
		}
	}
	return pkgs, unfree, nil
}

// Render produces the per-instance wrapper flake.nix content for cfg,
// importing templateRef's homeManagerModules.<name> as the template
// module, plus a synthetic module for cfg.Packages and one per
// cfg.Flakes[] entry (its packages, and its homeManagerModules.default
// if Modules is true).
func Render(cfg config.Config, templateRef string) (string, error) {
	url, name := splitFlakeRef(templateRef)
	agentPkgs, unfreePkgs, err := resolveAgents(cfg.Agents)
	if err != nil {
		return "", err
	}
	data := renderData{
		TemplateURL:    url,
		TemplateName:   templateModuleName(name, cfg.Arch),
		System:         NixSystem(cfg.Arch),
		Tailscale:      cfg.Tailscale,
		Packages:       cfg.Packages,
		AgentPackages:  agentPkgs,
		UnfreePackages: unfreePkgs,
		Flakes:         cfg.Flakes,
	}
	if err := validateRenderData(data); err != nil {
		return "", err
	}
	var out strings.Builder
	if err := renderTmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

// validNixIdent matches nixpkgs attribute names (package names, module
// names): letters, digits, '_', '+', '.', '-'. Used for values the
// template embeds as a Nix string-literal *key* (pkgs."name"), where
// the legal charset is narrow enough to just allowlist.
var validNixIdent = regexp.MustCompile(`^[A-Za-z0-9_+.-]+$`)

// validateNixIdent rejects a package/module name outside validNixIdent's
// charset -- in particular '"' and '$', which could otherwise break out
// of or interpolate into the Nix string literal it's embedded in.
func validateNixIdent(kind, name string) error {
	if !validNixIdent.MatchString(name) {
		return fmt.Errorf("%s %q is not a valid Nix package/attribute name (only letters, digits, '_', '+', '.', '-' allowed)", kind, name)
	}
	return nil
}

// validateNixString rejects a value (a flake URL) that isn't a valid
// Nix identifier but is still embedded as a Nix string-literal value.
// Flake refs legitimately use ':', '/', '?', '=', '&', '#', so this
// only blocks the two ways out of a Nix double-quoted string: a literal
// '"' closing it early, and Nix's own "${...}" interpolation syntax,
// which evaluates arbitrary Nix without even needing to close the quote.
func validateNixString(kind, value string) error {
	if strings.Contains(value, `"`) || strings.Contains(value, "${") {
		return fmt.Errorf("%s %q is not safe to embed in a Nix string literal (contains '\"' or '${')", kind, value)
	}
	return nil
}

func validateRenderData(data renderData) error {
	if err := validateNixString("template url", data.TemplateURL); err != nil {
		return err
	}
	if err := validateNixIdent("template name", data.TemplateName); err != nil {
		return err
	}
	for _, pkg := range data.Packages {
		if err := validateNixIdent("package", pkg); err != nil {
			return err
		}
	}
	for _, f := range data.Flakes {
		if err := validateNixString("flake url", f.Url); err != nil {
			return err
		}
		for _, pkg := range f.Packages {
			if err := validateNixIdent("flake package", pkg); err != nil {
				return err
			}
		}
	}
	return nil
}
