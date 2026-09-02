package provisioning

import "strings"

// defaultTemplatesRef is this repo's own templates/ flake, floated on
// the default branch (not a version tag) — see the Provisioning
// design spec for why template fixes shouldn't need a cloudlab
// release.
const defaultTemplatesRef = "github:jskswamy/cloudlab?dir=templates"

// builtinTemplates is the set of template names ResolveTemplateRef
// expands against defaultTemplatesRef. Anything else is assumed to
// already be a full "url#name" flake ref and is returned as-is.
var builtinTemplates = map[string]bool{
	"python": true,
	"docker": true,
}

// ResolveTemplateRef expands a built-in template name ("python",
// "docker") to its default flake ref, with arch's Nix system appended
// to the output name (matching templates/flake.nix's
// "<name>-<system>" homeConfigurations naming). Any other template
// value is assumed to already be a complete flake ref and is returned
// unchanged, ignoring arch.
func ResolveTemplateRef(template, arch string) string {
	if !builtinTemplates[template] {
		return template
	}
	return defaultTemplatesRef + "#" + template + "-" + NixSystem(arch)
}

// NixSystem maps a cloudlab.pkl arch value to the Nix system string
// used for per-system flake outputs. Every cloudlab instance is
// Linux; only the CPU part varies. Empty or unrecognized values
// default to x86_64-linux, matching cloudlab.pkl's own arch default.
func NixSystem(arch string) string {
	if arch == "arm64" {
		return "aarch64-linux"
	}
	return "x86_64-linux"
}

// templateModuleName strips the "-<system>" suffix ResolveTemplateRef
// appends to a built-in template's output name for homeConfigurations
// naming. homeManagerModules stays flat regardless of arch (see
// templates/flake.nix), so module lookups need the suffix removed.
func templateModuleName(name, arch string) string {
	return strings.TrimSuffix(name, "-"+NixSystem(arch))
}

// splitFlakeRef splits a home-manager-style "url#name" reference into
// its flake URL and output name. A ref with no "#" returns the whole
// string as url and an empty name.
func splitFlakeRef(ref string) (url, name string) {
	i := strings.LastIndex(ref, "#")
	if i < 0 {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}
