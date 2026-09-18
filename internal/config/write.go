package config

import (
	"fmt"
	"sort"
	"strings"
)

// Values is a sparse set of config field values keyed by their name in
// Config.pkl. Only the fields present are rendered.
//
// Sparseness is the whole point, and the reason this is not a Config:
// Config gives arch, image, tailscale, beads and the listings their
// schema defaults on load, so it cannot say whether a file declared a
// field or merely inherited it. The wizard and the preset store both
// need that distinction — writing a field nobody asked about turns
// "change one package" into a diff touching every line.
//
// Permitted value types are string, bool, []string and []Flake, matched
// per field by fieldKinds; Render rejects anything else rather than
// emitting pkl that will not parse.
type Values map[string]any

// Field names, spelled once here so the renderer, the wizard's routing
// table and the preset store cannot drift from each other or from
// Config.pkl.
const (
	FieldBasePath     = "basePath"
	FieldRegion       = "region"
	FieldSize         = "size"
	FieldTemplate     = "template"
	FieldArch         = "arch"
	FieldImage        = "image"
	FieldTailscale    = "tailscale"
	FieldBeads        = "beads"
	FieldSSHKeys      = "sshKeys"
	FieldPackages     = "packages"
	FieldAgents       = "agents"
	FieldInstructions = "instructions"
	FieldFlakes       = "flakes"
)

// fieldOrder is Config.pkl's own declaration order. Rendered files
// follow it regardless of the order a caller happened to build Values
// in, so that two runs producing the same answers produce byte-identical
// files and a re-render shows up as a diff only where a value changed.
var fieldOrder = []string{
	FieldBasePath,
	FieldRegion,
	FieldSize,
	FieldTemplate,
	FieldArch,
	FieldImage,
	FieldTailscale,
	FieldBeads,
	FieldSSHKeys,
	FieldPackages,
	FieldAgents,
	FieldInstructions,
	FieldFlakes,
}

type fieldKind int

const (
	kindString fieldKind = iota
	kindBool
	kindStringList
	kindFlakeList
)

var fieldKinds = map[string]fieldKind{
	FieldBasePath:     kindString,
	FieldRegion:       kindString,
	FieldSize:         kindString,
	FieldTemplate:     kindString,
	FieldArch:         kindString,
	FieldImage:        kindString,
	FieldTailscale:    kindBool,
	FieldBeads:        kindString,
	FieldSSHKeys:      kindStringList,
	FieldPackages:     kindStringList,
	FieldAgents:       kindStringList,
	FieldInstructions: kindStringList,
	FieldFlakes:       kindFlakeList,
}

// KnownField reports whether name is a field of Config.pkl.
func KnownField(name string) bool {
	_, ok := fieldKinds[name]
	return ok
}

// Fields returns every Config.pkl field name, in schema order.
func Fields() []string {
	return append([]string{}, fieldOrder...)
}

// Render turns a sparse set of field values into the text of a pkl file
// that amends bivouac's schema — a project bivouac.pkl, a personal
// base.pkl or a preset, which are all the same shape.
//
// Hand-written rather than generated: pkl-go's codegen produces readers
// only, so this is the counterpart to Resolve and the one place that
// knows how a value of each kind is spelled.
//
// No `amends` line is emitted. injectSchema supplies the schema when the
// file is read back, and a file carrying its own amends is rejected
// there — so emitting one would make every file this writes unloadable.
func Render(v Values) (string, error) {
	for _, name := range sortedKeys(v) {
		if !KnownField(name) {
			return "", fmt.Errorf("rendering config: %q is not a field of bivouac's schema", name)
		}
	}

	var b strings.Builder
	for _, name := range fieldOrder {
		value, ok := v[name]
		if !ok {
			continue
		}
		if err := renderField(&b, name, value); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func renderField(b *strings.Builder, name string, value any) error {
	switch fieldKinds[name] {
	case kindString:
		s, ok := value.(string)
		if !ok {
			return typeError(name, "a string", value)
		}
		fmt.Fprintf(b, "%s = %s\n", name, quote(s))
	case kindBool:
		t, ok := value.(bool)
		if !ok {
			return typeError(name, "a boolean", value)
		}
		fmt.Fprintf(b, "%s = %t\n", name, t)
	case kindStringList:
		items, ok := value.([]string)
		if !ok {
			return typeError(name, "a list of strings", value)
		}
		renderStringListing(b, "", name, items)
	case kindFlakeList:
		flakes, ok := value.([]Flake)
		if !ok {
			return typeError(name, "a list of Flake", value)
		}
		renderFlakes(b, name, flakes)
	}
	return nil
}

// renderStringListing writes `name { ... }`, collapsing the empty case
// to one line — an empty listing over three lines reads as an unfinished
// edit rather than a deliberate "none".
func renderStringListing(b *strings.Builder, indent, name string, items []string) {
	if len(items) == 0 {
		fmt.Fprintf(b, "%s%s {}\n", indent, name)
		return
	}
	fmt.Fprintf(b, "%s%s {\n", indent, name)
	for _, item := range items {
		fmt.Fprintf(b, "%s  %s\n", indent, quote(item))
	}
	fmt.Fprintf(b, "%s}\n", indent)
}

// renderFlakes writes the Listing<Flake> as `new Flake { ... }` elements.
//
// The class is named explicitly because these files amend the schema
// rather than declare it: inside an amended `flakes { ... }` block a bare
// `new {}` builds a Dynamic, which pkl then refuses to coerce to Flake.
func renderFlakes(b *strings.Builder, name string, flakes []Flake) {
	if len(flakes) == 0 {
		fmt.Fprintf(b, "%s {}\n", name)
		return
	}
	fmt.Fprintf(b, "%s {\n", name)
	for _, f := range flakes {
		b.WriteString("  new Flake {\n")
		fmt.Fprintf(b, "    url = %s\n", quote(f.Url))
		renderStringListing(b, "    ", "packages", f.Packages)
		renderStringListing(b, "    ", "modules", f.Modules)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
}

// quote renders a Go string as a pkl string literal. Backslash is
// escaped first and unconditionally because pkl reads `\(` as the start
// of an interpolated expression: an unescaped backslash in a value does
// not merely look wrong, it changes what the rest of the line means.
func quote(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\t", `\t`,
		"\r", `\r`,
	)
	return `"` + r.Replace(s) + `"`
}

func typeError(name, want string, got any) error {
	return fmt.Errorf("rendering config: field %q wants %s, got %T", name, want, got)
}

func sortedKeys(v Values) []string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
