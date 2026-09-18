package config

// Resolved is a configuration that has been through Resolve: the required
// fields are known to be set, and carried as plain strings rather than the
// pointers the generated struct uses.
//
// The embedded Config is the whole of what bivouac.pkl declares, so a field
// added to the schema reaches callers without being restated here. Only the
// three fields whose presence Resolve actually proves are lifted out, and
// they deliberately shadow their pointer-shaped counterparts: cfg.Region is
// the string, and cfg.Config.Region is the *string it came from.
//
// That shadowing is the point. Six call sites used to write *cfg.Region on
// the strength of a guarantee that lived in a comment and in validate's
// implementation; now the guarantee is in the type, and the code that
// assumed it either says so or does not compile.
type Resolved struct {
	Config

	// Region, Size and Template are non-empty for any Resolved that came
	// from Resolve. Nothing else constructs one in production.
	Region   string
	Size     string
	Template string
}

// resolved lifts a validated Config into a Resolved.
//
// Unexported, and called only after validate has returned nil: a Resolved
// built any other way carries no guarantee, so there is deliberately no
// exported way to make one out of an unchecked Config.
func resolved(c Config) Resolved {
	return Resolved{
		Config:   c,
		Region:   *c.Region,
		Size:     *c.Size,
		Template: *c.Template,
	}
}
