package provisioning

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/jskswamy/cloudlab/internal/config"
)

// NeedsRender reports whether cfg's packages/flakes require a
// per-instance wrapper flake, rather than using the template ref
// as-is. See the Provisioning design spec's render-trigger rule.
func NeedsRender(cfg config.Config) bool {
	return len(cfg.Packages) > 0 || len(cfg.Flakes) > 0
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
      pkgs = nixpkgs.legacyPackages."{{.System}}";
      modules = [
        template.homeManagerModules."{{.TemplateName}}"
{{if .Packages}}        ({ pkgs, ... }: { home.packages = [ {{range .Packages}}pkgs."{{.}}" {{end}}]; })
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
	Packages     []string
	Flakes       []config.Flake
}

// Render produces the per-instance wrapper flake.nix content for cfg,
// importing templateRef's homeManagerModules.<name> as the template
// module, plus a synthetic module for cfg.Packages and one per
// cfg.Flakes[] entry (its packages, and its homeManagerModules.default
// if Modules is true).
func Render(cfg config.Config, templateRef string) (string, error) {
	url, name := splitFlakeRef(templateRef)
	data := renderData{
		TemplateURL:  url,
		TemplateName: templateModuleName(name, cfg.Arch),
		System:       NixSystem(cfg.Arch),
		Packages:     cfg.Packages,
		Flakes:       cfg.Flakes,
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
