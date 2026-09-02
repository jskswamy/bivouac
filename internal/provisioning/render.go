package provisioning

import (
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
	var out strings.Builder
	if err := renderTmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}
