package provisioning

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/jskswamy/bivouac/internal/config"
)

// NeedsRender reports whether cfg requires a per-instance wrapper
// flake, rather than using the template ref as-is. See the Provisioning
// design spec's render-trigger rule.
//
// Tailscale counts alongside packages/flakes because the rendered flake
// is the only place bivouac.tailscale is ever set (see renderTmpl);
// the shared template defaults it to false. Left out of this condition,
// "tailscale = true" in a config with no packages and no flakes renders
// nothing, so the template's default stands and the tailscaled unit is
// never installed -- the flag silently does nothing at all.
func NeedsRender(cfg config.Config) bool {
	return len(cfg.Packages) > 0 || len(cfg.Flakes) > 0 || cfg.Tailscale || len(cfg.Agents) > 0 || len(cfg.Settings) > 0
}

// NixpkgsRef is the nixpkgs the rendered flake pins, and so the one a
// `packages` entry has to exist in. Named once here because validation
// has to check the same tree the switch will build against -- two
// spellings could disagree, and the one that would be wrong is the one
// that only says "ok".
const NixpkgsRef = "github:NixOS/nixpkgs/nixos-unstable"

var renderTmpl = template.Must(template.New("flake").Funcs(template.FuncMap{
	"nixPath":  nixPath,
	"nixValue": nixValue,
}).Parse(`{
  inputs = {
    nixpkgs.url = "` + NixpkgsRef + `";
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
        { bivouac.tailscale = {{.Tailscale}}; }
{{if .Packages}}        ({ pkgs, ... }: { home.packages = [ {{range .Packages}}pkgs."{{.}}" {{end}}]; })
{{end}}{{if .AgentPackages}}        ({ pkgs, ... }: { home.packages = [ {{range .AgentPackages}}pkgs."{{.}}" {{end}}]; })
{{end}}{{range $i, $f := .Flakes}}{{if $f.Packages}}        { home.packages = [ {{range $f.Packages}}flake{{$i}}.packages."{{$.System}}"."{{.}}" {{end}}]; }
{{end}}{{range $f.Modules}}        flake{{$i}}.homeManagerModules.{{nixPath .}}
{{end}}{{end}}{{if .Settings}}        ({ ... }: { {{range $k, $v := .Settings}}{{nixPath $k}} = {{nixValue $v}}; {{end}}})
{{end}}      ];
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
	Settings       map[string]any
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
// module, plus a synthetic module for cfg.Packages, one per
// cfg.Flakes[] entry's packages and each homeManagerModules path it
// names, and a final one for cfg.Settings.
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
		Settings:       cfg.Settings,
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

// nixPath turns a dot-separated string into a Nix attribute path with
// every segment quoted: "tools.git" becomes "tools"."git". Nix source
// requires this: an unquoted hyphenated segment (`a.git-tools`) parses
// as subtraction, not nested attribute access, so every segment is
// quoted unconditionally rather than only when it would otherwise be
// ambiguous. There is no leading dot, so the same path works both after
// an expression (x.<path>) and as a binding's left-hand side
// (<path> = v;), where a leading dot is a syntax error.
func nixPath(dotted string) string {
	segs := strings.Split(dotted, ".")
	for i, seg := range segs {
		segs[i] = `"` + seg + `"`
	}
	return strings.Join(segs, ".")
}

// nixValue renders a settings value (pkl-go's decoding of a
// String|Boolean|Int|Listing<String> union) as a Nix literal. Lists go
// through config.SettingList, which accepts both pkl-go's boxed
// []interface{} and a Go-built []string.
func nixValue(v any) (string, error) {
	if list, ok, err := config.SettingList(v); ok {
		if err != nil {
			return "", fmt.Errorf("settings value: %w", err)
		}
		parts := make([]string, len(list))
		for i, s := range list {
			q, err := quotedNixString(s)
			if err != nil {
				return "", err
			}
			parts[i] = q
		}
		return "[ " + strings.Join(parts, " ") + " ]", nil
	}
	switch x := v.(type) {
	case string:
		return quotedNixString(x)
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	default:
		return "", fmt.Errorf("settings value %v: unsupported type %T", v, v)
	}
}

// quotedNixString renders a settings string, alone or as a list
// element, as a Nix string literal, rejecting anything that could leave
// the literal rather than escaping it.
func quotedNixString(s string) (string, error) {
	if err := validateNixString("settings value", s); err != nil {
		return "", err
	}
	return `"` + s + `"`, nil
}

// validateNixPath applies validateNixIdent to each segment of a dotted
// path rather than to the whole string. The charset admits '.', so a
// whole-string check would pass "tools..git" or ".git", which nixPath
// would render with a quoted empty attribute name.
func validateNixPath(kind, dotted string) error {
	for _, seg := range strings.Split(dotted, ".") {
		if seg == "" {
			return fmt.Errorf("%s %q has an empty path segment", kind, dotted)
		}
		if err := validateNixIdent(kind, seg); err != nil {
			return err
		}
	}
	return nil
}

// validateNixString rejects a value (a flake URL, a settings string)
// that isn't a valid Nix identifier but is still embedded as a Nix
// string-literal value. Flake refs legitimately use ':', '/', '?', '=',
// '&', '#', so this only blocks the ways out of a Nix double-quoted
// string: a literal '"' closing it early, a '\' escaping the closing
// quote Render adds, and Nix's own "${...}" interpolation syntax, which
// evaluates arbitrary Nix without even needing to close the quote.
func validateNixString(kind, value string) error {
	if strings.ContainsAny(value, `"\`) || strings.Contains(value, "${") {
		return fmt.Errorf("%s %q is not safe to embed in a Nix string literal (contains '\"', '\\' or '${')", kind, value)
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
		for _, m := range f.Modules {
			if err := validateNixPath("flake module", m); err != nil {
				return err
			}
		}
	}
	for k := range data.Settings {
		if err := validateNixPath("settings key", k); err != nil {
			return err
		}
	}
	return nil
}
