// Package wizard holds everything the interactive config flow knows
// that is not a form: which questions exist, where each answer belongs,
// and what a preset keeps.
//
// Separate from cmd on purpose. The huh forms are the part that cannot
// be driven from a test, so nothing that can be decided without a
// terminal is decided inside one.
package wizard

import (
	"fmt"
	"reflect"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provisioning"
)

// Destination is the file an answer is written to.
type Destination int

const (
	// Personal is ~/.config/cloudlab/base.pkl: answers that describe
	// the user, not the project.
	Personal Destination = iota
	// Project is ./cloudlab.pkl: answers that describe the project and
	// are committed with it.
	Project
)

func (d Destination) String() string {
	if d == Personal {
		return "base.pkl"
	}
	return "cloudlab.pkl"
}

// destinations maps every schema field to the file it belongs in.
//
// The split is by the nature of the field rather than by asking the
// user, because the right answer never varies: cloudlab.pkl is
// committed, so stamping a fingerprint or a personal machine size into
// it hands the next person who clones the repo the author's key and the
// author's sizing. basePath is deliberately absent — it selects which
// base file to merge, and a flow that could write it could repoint its
// own base out from under itself.
var destinations = map[string]Destination{
	config.FieldRegion:    Personal,
	config.FieldSize:      Personal,
	config.FieldSSHKeys:   Personal,
	config.FieldTemplate:  Project,
	config.FieldArch:      Project,
	config.FieldImage:     Project,
	config.FieldTailscale: Project,
	config.FieldBeads:     Project,
	config.FieldPackages:  Project,
	config.FieldAgents:    Project,
	config.FieldFlakes:    Project,
}

// Route reports which file field's answer belongs in.
func Route(field string) (Destination, error) {
	d, ok := destinations[field]
	if !ok {
		return 0, fmt.Errorf("no destination for field %q", field)
	}
	return d, nil
}

// Split divides a run's answers into the two files they are written to.
func Split(answers config.Values) (personal, project config.Values, err error) {
	personal, project = config.Values{}, config.Values{}
	for field, value := range answers {
		d, err := Route(field)
		if err != nil {
			return nil, nil, err
		}
		if d == Personal {
			personal[field] = value
		} else {
			project[field] = value
		}
	}
	return personal, project, nil
}

// PersonalFields returns the fields that belong in base.pkl, in schema
// order. Used to spot them in a project file that predates the split.
func PersonalFields() []string {
	var fields []string
	for _, field := range config.Fields() {
		if d, err := Route(field); err == nil && d == Personal {
			fields = append(fields, field)
		}
	}
	return fields
}

// Kind is how a question is asked.
type Kind int

const (
	// Text takes one free-form string.
	Text Kind = iota
	// TextList takes a free-form list, one entry per line.
	TextList
	// Select takes one of Options.
	Select
	// MultiSelect takes any number of Options.
	MultiSelect
	// Confirm takes yes or no.
	Confirm
)

func (k Kind) String() string {
	switch k {
	case Text:
		return "text"
	case TextList:
		return "text list"
	case Select:
		return "select"
	case MultiSelect:
		return "multi-select"
	case Confirm:
		return "confirm"
	}
	return "unknown"
}

// Question is one field's question, as data. The form layer decides how
// to render it; everything about which questions exist and what they
// accept is decided here, where it can be tested.
type Question struct {
	// Field is the Config.pkl field the answer is written to.
	Field string
	// Title is the prompt.
	Title string
	// Description is the line under the prompt.
	Description string
	// Kind is how the answer is collected.
	Kind Kind
	// Options are the choices, for Select and MultiSelect.
	Options []string
	// Freeform lets a Select accept a value outside Options — the
	// bring-your-own case, where the closed list is a convenience
	// rather than the whole set.
	Freeform bool
	// Placeholder is an example value, for Text and TextList.
	Placeholder string
}

// PersonalQuestions are asked once, on the first run that finds no
// base.pkl, and their answers describe the user's own machine
// preferences rather than any project.
//
// region and size take typed slugs rather than a list fetched from the
// provider: the account's real choices need a token, and a flow that
// cannot start without credentials is a worse first experience than one
// that checks the value when it is used. The read-back through
// config.Resolve catches a malformed file; a wrong-but-valid slug is
// caught by the provider at up.
func PersonalQuestions() []Question {
	return []Question{
		{
			Field:       config.FieldRegion,
			Title:       "Which region?",
			Description: "A DigitalOcean region slug.",
			Kind:        Text,
			Placeholder: "nyc3",
		},
		{
			Field:       config.FieldSize,
			Title:       "What size instance?",
			Description: "A DigitalOcean size slug. Projects that need more can override it.",
			Kind:        Text,
			Placeholder: "s-1vcpu-1gb",
		},
		{
			Field:       config.FieldSSHKeys,
			Title:       "Which SSH keys?",
			Description: "Fingerprints or IDs of keys already registered with DigitalOcean, separated by commas.",
			Kind:        TextList,
			Placeholder: "aa:bb:cc:...",
		},
	}
}

// ProjectQuestions describe the shape of this project's instance and
// are written to the committed cloudlab.pkl.
//
// The template question is a Select over the built-ins with Freeform
// set, because a template may also be any flake ref. It is single-value
// today; when the profiles rename lands it becomes a MultiSelect, and
// this is the one question that changes.
func ProjectQuestions() []Question {
	return []Question{
		{
			Field:       config.FieldTemplate,
			Title:       "Which toolchain?",
			Description: "A built-in template, or a flake ref of your own.",
			Kind:        Select,
			Options:     provisioning.BuiltinTemplates(),
			Freeform:    true,
			Placeholder: "github:you/flake#devbox",
		},
		{
			Field:       config.FieldAgents,
			Title:       "Which coding agents?",
			Description: "Installed by short name; several are unfree and are permitted individually.",
			Kind:        MultiSelect,
			// Mirrors Config.pkl's agents union. A wizard test reads
			// the schema and fails if the two sets drift.
			Options: []string{"claude", "codex", "copilot", "cursor", "opencode", "pi"},
		},
		{
			Field:       config.FieldPackages,
			Title:       "Any extra packages?",
			Description: "nixpkgs attribute names, separated by commas.",
			Kind:        TextList,
			Placeholder: "ripgrep",
		},
		{
			Field:       config.FieldBeads,
			Title:       "How should beads sync?",
			Description: "session rides the session's own git remote; dolthub also syncs the external remote; off wires nothing.",
			Kind:        Select,
			// Mirrors Config.pkl's beads union; see agents above.
			Options: []string{"session", "dolthub", "off"},
		},
		{
			Field:       config.FieldTailscale,
			Title:       "Join the instance to your tailnet?",
			Description: "Gives the instance a tailnet address to reach it on.",
			Kind:        Confirm,
		},
	}
}

// presetShape is what a preset keeps unconditionally: the project's
// shape, minus anything personal.
//
// tailscale is included although the design spec's preset section lists
// only template, agents, packages and beads. It is a project field
// everywhere else in that same spec, and dropping it would mean a
// preset saved from a tailnet-joined project quietly produces one that
// is not. Flagged rather than silently diverged: see cloudlab-pwg.2.
var presetShape = []string{
	config.FieldTemplate,
	config.FieldAgents,
	config.FieldPackages,
	config.FieldBeads,
	config.FieldTailscale,
}

// PresetValues reduces a run's answers to what is worth saving as a
// reusable preset, given the personal base the run started from.
//
// Shape is always kept. region and size are kept only where this run
// overrode base, which is the one case a shape genuinely implies
// sizing — a toolchain that needs headroom, picked on a small base,
// otherwise fails at provisioning with nothing having warned. Capturing
// them unconditionally would instead reset the user's own defaults every
// time the preset is used.
//
// sshKeys is never kept. A preset is a shape to reuse across projects;
// a key fingerprint identifies one person.
func PresetValues(answers, base config.Values) config.Values {
	preset := config.Values{}
	for _, field := range presetShape {
		if v, ok := answers[field]; ok {
			preset[field] = v
		}
	}
	for _, field := range []string{config.FieldRegion, config.FieldSize} {
		v, ok := answers[field]
		if !ok {
			continue
		}
		if b, inBase := base[field]; !inBase || !reflect.DeepEqual(b, v) {
			preset[field] = v
		}
	}
	return preset
}
