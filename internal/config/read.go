package config

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ReadValues loads the file at path and returns exactly the fields it
// declares, as the sparse Values that Render consumes. It is the inverse
// of Render, and the reader the wizard and the preset store use where
// Resolve would be wrong: it merges no base file and enforces no
// required fields, because a base.pkl or a preset is partial by design.
//
// Presence comes from the file's text and values come from pkl. Neither
// half is sufficient alone: a loaded Config gives arch, image,
// tailscale, beads and the listings their schema defaults, so it cannot
// say whether the file declared them — and returning a default the file
// never mentioned would make editing one package rewrite the file with
// lines its author chose to leave out.
func ReadValues(ctx context.Context, path string) (Values, error) {
	// #nosec G304 -- path is the cloudlab.pkl/base.pkl/preset the caller
	// asked to read; see loadResolved for the same rationale.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg, err := loadResolved(ctx, path)
	if err != nil {
		return nil, err
	}

	values := Values{}
	for name := range declaredFields(raw) {
		if v, ok := fieldValue(cfg, name); ok {
			values[name] = v
		}
	}
	return values, nil
}

// fieldValue extracts one field from a loaded Config by its schema name.
// The optional fields report false when unset: the text said the file
// declares them, but `region = null` declares nothing worth writing back.
func fieldValue(c Config, name string) (any, bool) {
	switch name {
	case FieldBasePath:
		return derefString(c.BasePath)
	case FieldRegion:
		return derefString(c.Region)
	case FieldSize:
		return derefString(c.Size)
	case FieldTemplate:
		return derefString(c.Template)
	case FieldArch:
		return c.Arch, true
	case FieldImage:
		return c.Image, true
	case FieldTailscale:
		return c.Tailscale, true
	case FieldBeads:
		return c.Beads, true
	case FieldSSHKeys:
		if c.SshKeys == nil {
			return nil, false
		}
		return append([]string{}, *c.SshKeys...), true
	case FieldPackages:
		return append([]string{}, c.Packages...), true
	case FieldAgents:
		return append([]string{}, c.Agents...), true
	case FieldInstructions:
		return append([]string{}, c.Instructions...), true
	case FieldFlakes:
		return append([]Flake{}, c.Flakes...), true
	}
	return nil, false
}

func derefString(p *string) (any, bool) {
	if p == nil {
		return nil, false
	}
	return *p, true
}

// fieldDefaults is the value each field takes when no file declares it,
// mirroring the defaults in Config.pkl.
//
// A copy rather than a lookup, because reading it out of the schema
// means evaluating pkl, and the callers that need it are deciding
// whether to write a line. A test loads an empty config through pkl and
// fails if this table and the schema have drifted apart.
//
// The fields absent here -- basePath, region, size, template, sshKeys --
// are the ones the schema leaves null, so any value for them says
// something the schema would not.
var fieldDefaults = map[string]any{
	FieldArch:         "x86_64",
	FieldImage:        "ubuntu-24-04-x64",
	FieldTailscale:    false,
	FieldBeads:        "session",
	FieldPackages:     []string{},
	FieldAgents:       []string{},
	FieldInstructions: []string{},
	FieldFlakes:       []Flake{},
}

// IsDefault reports whether value is what field would resolve to anyway,
// so that writing it would add a line saying nothing.
//
// Used to keep a written config to what its author actually chose. An
// unset listing and an empty one are the same thing here: the schema's
// default for each is empty, and a caller that collected no entries has
// nothing to record either way.
func IsDefault(field string, value any) bool {
	def, ok := fieldDefaults[field]
	if !ok {
		return false
	}
	switch d := def.(type) {
	case []string:
		v, isList := value.([]string)
		return isList && len(v) == len(d)
	case []Flake:
		v, isList := value.([]Flake)
		return isList && len(v) == len(d)
	default:
		return def == value
	}
}

// declarationPattern matches a top-level property declaration: a name
// followed by `=` for a scalar or `{` for a listing being amended.
var declarationPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*[={]`)

// declaredFields reports which of the schema's fields raw declares.
//
// Text rather than a second, all-optional copy of the schema: two
// schemas would have to be kept in step by hand, and the one that drifts
// is the one nothing loads. The cost is that this must understand where
// pkl code ends — hence sanitize, and the depth counting that keeps a
// Flake's own `packages` from being read as the module's.
func declaredFields(raw []byte) map[string]struct{} {
	found := map[string]struct{}{}
	depth, inBlockComment := 0, false

	for _, line := range strings.Split(string(raw), "\n") {
		var code string
		code, inBlockComment = sanitize(line, inBlockComment)

		if depth == 0 {
			if m := declarationPattern.FindStringSubmatch(strings.TrimSpace(code)); m != nil && KnownField(m[1]) {
				found[m[1]] = struct{}{}
			}
		}
		depth += strings.Count(code, "{") - strings.Count(code, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return found
}

// sanitize strips comments from one line and empties every string
// literal in it, leaving code whose braces are all structural. Returns
// the line and whether a block comment is still open at its end.
//
// Emptying rather than removing string literals keeps `region = ""`
// looking like a declaration, which it is.
func sanitize(line string, inBlockComment bool) (string, bool) {
	var out strings.Builder
	inString := false

	for i := 0; i < len(line); i++ {
		switch {
		case inBlockComment:
			if strings.HasPrefix(line[i:], "*/") {
				inBlockComment = false
				i++
			}
		case inString:
			if line[i] == '\\' {
				i++ // whatever follows is escaped, `"` included
				continue
			}
			if line[i] == '"' {
				inString = false
				out.WriteByte('"')
			}
		case strings.HasPrefix(line[i:], "//"):
			return out.String(), false
		case strings.HasPrefix(line[i:], "/*"):
			inBlockComment = true
			i++
		case line[i] == '"':
			inString = true
			out.WriteByte('"')
		default:
			out.WriteByte(line[i])
		}
	}
	return out.String(), inBlockComment
}
