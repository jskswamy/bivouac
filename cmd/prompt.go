package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/jskswamy/cloudlab/internal/wizard"
)

// formPrompter asks the init flow's questions with huh forms.
//
// One form per question rather than one form for the whole flow: the
// flow decides what to ask next from what it has already learned --
// whether a preset was chosen, whether a file needs moving -- and a
// single form would have to know all of that itself. Keeping the
// decisions in the flow is what leaves this file with nothing to test.
type formPrompter struct{}

func newFormPrompter() prompter { return formPrompter{} }

// otherChoice is the escape hatch on a Select whose options are a
// convenience rather than the whole set — a template may be any flake
// ref, and refusing to accept one would make the wizard less capable
// than the file it writes.
const otherChoice = "Something else…"

func (formPrompter) Ask(q wizard.Question, current any) (any, error) {
	switch q.Kind {
	case wizard.Text:
		return runValidatedInput(q.Title, q.Description, q.Placeholder, asString(current), answerValidator(q))
	case wizard.TextList:
		return runTextList(q, asStrings(current))
	case wizard.Select:
		return runSelect(q, asString(current))
	case wizard.MultiSelect:
		return runMultiSelect(q, asStrings(current))
	case wizard.Confirm:
		value, _ := current.(bool)
		err := run(huh.NewConfirm().Title(q.Title).Description(q.Description).Value(&value))
		return value, err
	}
	return nil, fmt.Errorf("no form for question kind %v", q.Kind)
}

func (formPrompter) Choose(m meta, options []string, current string) (string, error) {
	value := current
	err := run(huh.NewSelect[string]().
		Title(m.Title).
		Description(m.Description).
		Options(selectOptions(options, current)...).
		Value(&value))
	return value, err
}

func (formPrompter) Confirm(m meta, current bool) (bool, error) {
	value := current
	err := run(huh.NewConfirm().Title(m.Title).Description(m.Description).Value(&value))
	return value, err
}

func (formPrompter) Input(m meta, current string) (string, error) {
	return runInput(m.Title, m.Description, "", current)
}

// manualKeyEntry is the sentinel option that opens a free-text entry.
//
// A NUL keeps it from ever colliding with a real fingerprint, which is
// colon-separated hex.
const manualKeyEntry = "\x00manual"

// AskKeys offers the discovered keys as a multi-select, plus a way to
// type one in.
//
// The free-text route stays at every tier: it is what the field accepted
// before any of this existed, and it is the only way to name a key that
// is on neither this machine nor the account. A wizard that could not do
// what the file it writes can do would be a step backwards.
func (formPrompter) AskKeys(offer wizard.KeyOffer) ([]string, error) {
	// Nothing was discovered: there is no list to choose from, so ask
	// for the value directly rather than showing an empty form.
	if len(offer.Choices) == 0 {
		typed, err := runInput("Which SSH keys?", "Fingerprints or IDs registered with your provider, separated by commas.", "aa:bb:cc:...", "")
		if err != nil {
			return nil, err
		}
		return splitList(typed), nil
	}

	options := make([]huh.Option[string], 0, len(offer.Choices)+1)
	var value []string
	for _, c := range offer.Choices {
		options = append(options, huh.NewOption(keyLabel(c), c.Fingerprint).Selected(c.Selected))
		if c.Selected {
			value = append(value, c.Fingerprint)
		}
	}
	options = append(options, huh.NewOption("Type a fingerprint or key ID…", manualKeyEntry))

	description := "Keys on this machine. cloudlab cannot check these against your account without a token, and an unregistered key makes `up` fail before anything is created."
	if offer.AccountKnown {
		description = "Keys on this machine and on your provider account."
	}

	if err := run(huh.NewMultiSelect[string]().
		Title("Which SSH keys should instances accept?").
		Description(description).
		Options(options...).
		Value(&value)); err != nil {
		return nil, err
	}

	chosen := make([]string, 0, len(value))
	var manual bool
	for _, fp := range value {
		if fp == manualKeyEntry {
			manual = true
			continue
		}
		chosen = append(chosen, fp)
	}
	if !manual {
		return chosen, nil
	}
	typed, err := runInput("Which other SSH keys?", "Fingerprints or IDs, separated by commas.", "aa:bb:cc:...", "")
	if err != nil {
		return nil, err
	}
	return append(chosen, splitList(typed)...), nil
}

// keyLabel is one row: what the key is called, and where it stands.
func keyLabel(c wizard.KeyChoice) string {
	if c.Detail == "" {
		return c.Label
	}
	return c.Label + "  (" + c.Detail + ")"
}

// runInput collects one line, trimmed: a stray space must not become
// part of a region slug or a preset name.
func runInput(title, description, placeholder, current string) (string, error) {
	return runValidatedInput(title, description, placeholder, current, nil)
}

// runValidatedInput is runInput with a check the form itself enforces.
//
// Handed to huh's own Validate rather than looped over out here: huh
// already keeps the field on screen with the message under it until the
// answer is acceptable, and a re-ask loop written here would have to
// reproduce that and would spin forever against the scripted prompter
// the tests drive, which returns one answer per field.
func runValidatedInput(title, description, placeholder, current string, validate func(string) error) (string, error) {
	value := current
	field := huh.NewInput().Title(title).Description(description).Value(&value)
	if placeholder != "" {
		field = field.Placeholder(placeholder)
	}
	if validate != nil {
		field = field.Validate(validate)
	}
	if err := run(field); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// answerValidator adapts a question's validator to the line huh hands
// it, which is not always one answer.
//
// A TextList is rendered as a single comma-separated input (see
// runTextList), so the check runs per entry after splitting: the
// validator a question carries is written against one value, and
// handing it the whole line would reject every list of more than one.
func answerValidator(q wizard.Question) func(string) error {
	if q.Validate == nil {
		return nil
	}
	if q.Kind == wizard.TextList {
		return func(line string) error {
			for _, entry := range splitList(line) {
				if err := q.Validate(entry); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return func(line string) error { return q.Validate(strings.TrimSpace(line)) }
}

// runTextList collects a list on one line, separated by commas.
//
// Not huh's multi-line Text: there, Enter submits and a new line is
// alt+enter or ctrl+j, so the obvious key ends the question after one
// entry and the way to type a second is not on screen. A comma-separated
// line has no hidden binding to discover.
func runTextList(q wizard.Question, current []string) (any, error) {
	value, err := runValidatedInput(q.Title, q.Description, listPlaceholder(q.Placeholder), strings.Join(current, ", "), answerValidator(q))
	if err != nil {
		return nil, err
	}
	return splitList(value), nil
}

// listPlaceholder shows a second entry, so that the separator is
// visible in the example rather than only in the description.
func listPlaceholder(one string) string {
	if one == "" {
		return ""
	}
	return one + ", ..."
}

// runSelect offers the known options, plus a way to type something else
// when the question allows it.
func runSelect(q wizard.Question, current string) (any, error) {
	options := q.Options
	if q.Freeform {
		options = append(append([]string{}, options...), otherChoice)
	}
	// A current value the option list does not contain is itself a
	// "something else" answer, and must stay selectable or editing a
	// project with a custom template would silently retype it.
	if q.Freeform && current != "" && !contains(q.Options, current) {
		options = append(options, current)
	}

	value := current
	if err := run(huh.NewSelect[string]().
		Title(q.Title).
		Description(q.Description).
		Options(selectOptions(options, current)...).
		Value(&value)); err != nil {
		return nil, err
	}
	if value != otherChoice {
		return value, nil
	}
	return runInput(q.Title, q.Placeholder, q.Placeholder, "")
}

func runMultiSelect(q wizard.Question, current []string) (any, error) {
	options := make([]huh.Option[string], 0, len(q.Options))
	for _, name := range q.Options {
		options = append(options, huh.NewOption(name, name).Selected(contains(current, name)))
	}
	value := append([]string{}, current...)
	if err := run(huh.NewMultiSelect[string]().
		Title(q.Title).
		Description(q.Description).
		Options(options...).
		Value(&value)); err != nil {
		return nil, err
	}
	return value, nil
}

func selectOptions(names []string, current string) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(names))
	for _, name := range names {
		options = append(options, huh.NewOption(name, name).Selected(name == current))
	}
	return options
}

// run shows one field. The key help stays on: it is the only place the
// form says how to move, select and submit.
func run(field huh.Field) error {
	return huh.NewForm(huh.NewGroup(field)).Run()
}

// splitList parses a comma-separated answer. Whitespace also separates,
// so a line typed with spaces and no commas still gives the list the
// user plainly meant; no value these fields accept contains either.
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asStrings(v any) []string {
	s, _ := v.([]string)
	return s
}
