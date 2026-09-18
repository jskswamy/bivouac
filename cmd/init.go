package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/identity"
	"github.com/jskswamy/bivouac/internal/preset"
	"github.com/jskswamy/bivouac/internal/wizard"
	"github.com/spf13/cobra"
)

// meta is a prompt the flow asks about itself rather than about a
// schema field. Key is what identifies it — to the tests, which script
// answers by key, and to a reader following which decision is which.
type meta struct {
	Key         string
	Title       string
	Description string
}

// The meta prompts, by key.
const (
	promptPersonalAction = "personal-action"
	promptPresetChoice   = "preset-choice"
	promptMovePersonal   = "move-personal"
	promptSavePreset     = "save-preset"
	promptPresetName     = "preset-name"
	promptUploadKey      = "upload-key"
	promptUploadKeyName  = "upload-key-name"
)

// Choices whose text the flow has to compare against.
const (
	personalUse      = "Use these"
	personalChange   = "Change them"
	presetStartFresh = "Start fresh"
)

// prompter collects answers. The huh forms implement it for real; the
// tests script it.
//
// The interface exists so that the four entry conditions, the routing
// of what is written where, and the read-back are all exercised without
// a terminal. A form is the one thing a test cannot drive, so the forms
// are given nothing to decide.
type prompter interface {
	// Ask puts one schema question to the user, starting from current.
	Ask(q wizard.Question, current any) (any, error)
	// Choose picks one of options.
	Choose(m meta, options []string, current string) (string, error)
	// Confirm asks a yes/no question.
	Confirm(m meta, current bool) (bool, error)
	// Input reads a single free-form line.
	Input(m meta, current string) (string, error)
	// AskKeys picks SSH keys from an offer, returning fingerprints.
	//
	// Its own method rather than a wizard.Question because the choices
	// are discovered rather than declared -- from the ssh-agent, from
	// ~/.ssh, and from the provider account -- and because picking one
	// can lead to registering it.
	AskKeys(offer wizard.KeyOffer) ([]string, error)
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create or edit this project's bivouac.pkl interactively",
		Long: "Walks through the same fields bivouac.pkl declares and writes the answers out:\n" +
			"personal choices to ~/.config/bivouac/base.pkl, this project's shape to\n" +
			"./bivouac.pkl. Offers to save the shape as a preset to start the next project from.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoFlag, _ := cmd.Flags().GetString("repo")
			root, err := repoRootFromFlag(cmd.Context(), repoFlag)
			if err != nil {
				return err
			}
			if err := requireTerminal(isInteractive()); err != nil {
				return err
			}
			return runInitFlow(cmd.Context(), cmd.OutOrStdout(), newFormPrompter(), root)
		},
	}
}

// requireTerminal refuses to open a form when there is nothing to type
// into.
//
// A form blocked on stdin that will never arrive is indistinguishable
// from a hang, and `up` -- which falls back to this flow -- is the
// command most likely to be scripted or piped. The message names
// `bivouac init` because running it at a terminal is what fixes both
// paths.
func requireTerminal(interactive bool) error {
	if interactive {
		return nil
	}
	return errors.New("this needs a terminal to ask its questions: run `bivouac init` from one, or write bivouac.pkl by hand (see docs/config.md)")
}

// repoRootFromFlag resolves the repository the config belongs to, the
// same way `up` does — one instance per repo means one bivouac.pkl
// per repo, at its root.
func repoRootFromFlag(ctx context.Context, repoFlag string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return identity.RepoRoot(ctx, cwd, repoFlag)
}

// runInitFlow is the whole interactive flow, shared by `bivouac init`
// and `up`'s fallback so the question set cannot drift between them.
//
// The order is: look at the project file first, because what it already
// declares decides which personal questions are worth asking; then
// settle the personal file; then the project one; then read back what
// was written.
func runInitFlow(ctx context.Context, out io.Writer, p prompter, root string) error {
	return runInitFlowWith(ctx, out, p, root, defaultKeySources())
}

// runInitFlowWith is runInitFlow with its SSH key inputs injected.
func runInitFlowWith(ctx context.Context, out io.Writer, p prompter, root string, keys keySources) error {
	basePath, err := config.DefaultBasePath()
	if err != nil {
		return err
	}
	projectPath := filepath.Join(root, "bivouac.pkl")

	existingProject, projectExists, err := readIfPresent(ctx, projectPath)
	if err != nil {
		return err
	}

	lifted, existingProject, err := offerMove(out, p, existingProject, projectExists)
	if err != nil {
		return err
	}

	base, err := settlePersonal(ctx, out, p, basePath, lifted, keys)
	if err != nil {
		return err
	}

	project, err := settleProject(ctx, out, p, projectPath, existingProject, projectExists)
	if err != nil {
		return err
	}

	// The read-back. A wizard that emits pkl which does not parse is
	// worse than no wizard, and nothing before this point would notice.
	if _, err := config.Resolve(ctx, projectPath); err != nil {
		return fmt.Errorf("the config just written does not load: %w", err)
	}
	printf(out, "Wrote %s\n", projectPath)

	return offerPreset(out, p, base, project)
}

// offerMove handles a project file that predates the split between
// personal and project settings, as this repository's own once did.
//
// It reports what it found and offers to lift those fields into
// base.pkl. Declining leaves them exactly where they are: restructuring
// a committed file as a side effect of editing one setting would turn a
// one-line change into a diff other people have to read. Both outcomes
// resolve to the same config, because a project file's scalars override
// base and its listings merge additively.
//
// That last sentence is what the offer rests on, and it is not true of
// every personal field -- see wizard.MovablePersonalFields, which is
// the set asked about here for that reason.
func offerMove(out io.Writer, p prompter, existing config.Values, exists bool) (lifted, remaining config.Values, err error) {
	if !exists {
		return config.Values{}, existing, nil
	}

	var found []string
	for _, field := range wizard.MovablePersonalFields() {
		if _, ok := existing[field]; ok {
			found = append(found, field)
		}
	}
	if len(found) == 0 {
		return config.Values{}, existing, nil
	}

	printf(out, "This project's bivouac.pkl sets %s, which are personal settings.\n", strings.Join(found, ", "))
	if _, committed := existing[config.FieldSSHKeys]; committed {
		printf(out, "sshKeys in particular is a fingerprint of your own key, committed for anyone who clones this repo.\n")
	}

	move, err := p.Confirm(meta{
		Key:         promptMovePersonal,
		Title:       "Move them to your personal base.pkl?",
		Description: "Resolves to the same config either way. Declining leaves the file as it is.",
	}, true)
	if err != nil {
		return nil, nil, err
	}
	if !move {
		return config.Values{}, existing, nil
	}

	lifted, remaining = config.Values{}, config.Values{}
	for field, value := range existing {
		if contains(found, field) {
			lifted[field] = value
		} else {
			remaining[field] = value
		}
	}
	return lifted, remaining, nil
}

// settlePersonal establishes the contents of base.pkl and returns them.
//
// Personal questions are asked once, on the first run that has nothing
// to go on. A base that already exists is summarised rather than
// re-asked, and fields lifted out of a project file answer the same
// questions without asking them again.
func settlePersonal(ctx context.Context, out io.Writer, p prompter, basePath string, lifted config.Values, keys keySources) (config.Values, error) {
	existing, exists, err := readIfPresent(ctx, basePath)
	if err != nil {
		return nil, err
	}

	values := merge(existing, lifted)
	asked := false
	switch {
	case len(lifted) > 0:
		// The user has just decided about these fields; asking again in
		// the same run would be asking twice.
	case exists:
		printf(out, "Your personal settings: %s\n", personalSummary(values))
		action, err := p.Choose(meta{
			Key:         promptPersonalAction,
			Title:       "Use these for this project?",
			Description: "They live in " + basePath + " and apply to every project.",
		}, []string{personalUse, personalChange}, personalUse)
		if err != nil {
			return nil, err
		}
		if action == personalChange {
			values, err = askPersonal(ctx, out, p, filepath.Dir(basePath), values, keys)
			if err != nil {
				return nil, err
			}
			asked = true
		}
	default:
		values, err = askPersonal(ctx, out, p, filepath.Dir(basePath), values, keys)
		if err != nil {
			return nil, err
		}
		asked = true
	}

	// A base that was trusted as-is (kept, or just lifted from a project
	// file) rather than freshly asked can still be missing a field
	// config.Resolve requires -- one written before that field existed,
	// or edited by hand. Left unchecked, that surfaces only at the
	// read-back in runInitFlowWith, after bivouac.pkl has already been
	// written. Catching it here asks for exactly what is missing instead.
	if !asked {
		if missing := missingQuestions(wizard.PersonalQuestions(filepath.Dir(basePath)), values); len(missing) > 0 {
			values, err = ask(p, missing, values)
			if err != nil {
				return nil, err
			}
		}
	}

	// Nothing answered and nothing already there: writing an empty
	// base.pkl would create a file that says nothing, and the project
	// file may well carry these settings itself.
	if len(values) == 0 {
		return values, nil
	}
	if !exists || !reflect.DeepEqual(values, existing) {
		// 0o600: base.pkl records personal choices and is not shared.
		if err := writeValues(basePath, values, 0o600); err != nil {
			return nil, err
		}
		printf(out, "Wrote %s\n", basePath)
	}
	return values, nil
}

// askPersonal asks the personal questions and then the SSH key one.
//
// sshKeys is asked apart from the others because it is not a question
// with a typed answer: its choices are discovered from the ssh-agent,
// from ~/.ssh and from the provider account, and choosing one can lead
// to registering it. Keeping it out of the declarative question list is
// what stops that machinery leaking into every other field.
func askPersonal(ctx context.Context, out io.Writer, p prompter, baseDir string, values config.Values, keys keySources) (config.Values, error) {
	values, err := ask(p, wizard.PersonalQuestions(baseDir), values)
	if err != nil {
		return nil, err
	}

	chosen, err := askSSHKeys(ctx, out, p, keys, currentKeys(values))
	if err != nil {
		return nil, err
	}
	if len(chosen) > 0 {
		values[config.FieldSSHKeys] = chosen
	} else {
		delete(values, config.FieldSSHKeys)
	}
	return values, nil
}

// settleProject establishes the contents of bivouac.pkl and returns
// them. A project with no file yet is offered the saved presets to
// start from; one that has a file is edited with its own values
// pre-filled.
func settleProject(ctx context.Context, out io.Writer, p prompter, projectPath string, existing config.Values, exists bool) (config.Values, error) {
	seed := existing
	if !exists {
		var err error
		seed, err = offerPresets(ctx, out, p)
		if err != nil {
			return nil, err
		}
	}

	values, err := ask(p, wizard.ProjectQuestions(), seed)
	if err != nil {
		return nil, err
	}
	// 0o644: bivouac.pkl is committed and read by whoever clones the
	// repo.
	if err := writeValues(projectPath, values, 0o644); err != nil {
		return nil, err
	}
	return values, nil
}

// offerPresets lets a new project start from a shape saved earlier.
//
// The chosen preset's fields are stamped into the new file rather than
// referenced from it: bivouac.pkl is committed, and a file that reads
// its settings from a preset only the author has is incomplete for
// everyone else -- silently, because Resolve skips a base it cannot
// find rather than failing.
func offerPresets(ctx context.Context, out io.Writer, p prompter) (config.Values, error) {
	presets, err := preset.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(presets) == 0 {
		return config.Values{}, nil
	}

	options := []string{presetStartFresh}
	byName := map[string]config.Values{}
	for _, saved := range presets {
		options = append(options, saved.Name)
		byName[saved.Name] = saved.Values
	}

	choice, err := p.Choose(meta{
		Key:         promptPresetChoice,
		Title:       "Start from a saved preset?",
		Description: "A preset's settings are copied into this project's file, not linked to it.",
	}, options, presetStartFresh)
	if err != nil {
		return nil, err
	}
	values, ok := byName[choice]
	if !ok {
		return config.Values{}, nil
	}
	printf(out, "Starting from preset %q.\n", choice)
	return merge(config.Values{}, values), nil
}

// offerPreset saves this run's shape for the next project to start
// from.
func offerPreset(out io.Writer, p prompter, base, project config.Values) error {
	values := wizard.PresetValues(merge(base, project), base)
	if len(values) == 0 {
		return nil
	}

	save, err := p.Confirm(meta{
		Key:         promptSavePreset,
		Title:       "Save this shape as a preset?",
		Description: "Offered as a starting point the next time you run this in a project with no config.",
	}, false)
	if err != nil || !save {
		return err
	}

	name, err := p.Input(meta{
		Key:         promptPresetName,
		Title:       "Name it:",
		Description: "Letters, digits, dashes and underscores.",
	}, "")
	if err != nil {
		return err
	}
	if err := preset.Save(name, values); err != nil {
		return err
	}
	printf(out, "Saved preset %q.\n", name)
	return nil
}

// ask puts questions to the user, each pre-filled from seed, and
// returns seed updated with the answers.
//
// An answer to a field the seed says nothing about is left out when it
// is blank or is simply the schema's default: `region = ""` and
// `beads = "session"` both add a line that changes nothing, and the
// written file should say what its author chose, not restate the
// schema. Pressing Enter through a question is not the same as asking
// for it to be recorded.
//
// A field the seed did have keeps its line whatever the answer, default
// included. Deleting a line from a file because the value it holds
// happens to match a default would be the same unasked-for restructuring
// the flow refuses elsewhere.
func ask(p prompter, questions []wizard.Question, seed config.Values) (config.Values, error) {
	values := merge(config.Values{}, seed)
	for _, q := range questions {
		current, had := values[q.Field]
		if !had {
			current = zeroFor(q.Kind)
		}
		answer, err := p.Ask(q, current)
		if err != nil {
			return nil, err
		}
		if !had && (isBlank(answer) || config.IsDefault(q.Field, answer)) {
			continue
		}
		values[q.Field] = answer
	}
	return values, nil
}

// missingQuestions is questions whose field values does not have.
func missingQuestions(questions []wizard.Question, values config.Values) []wizard.Question {
	var missing []wizard.Question
	for _, q := range questions {
		if _, ok := values[q.Field]; !ok {
			missing = append(missing, q)
		}
	}
	return missing
}

// zeroFor is the value a question starts from when nothing has answered
// it before, typed so the form knows which control to build.
func zeroFor(kind wizard.Kind) any {
	switch kind {
	case wizard.TextList, wizard.MultiSelect:
		return []string{}
	case wizard.Confirm:
		return false
	default:
		return ""
	}
}

// isBlank reports whether an answer says nothing. A false boolean is
// not blank: it is the answer "no".
func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []string:
		return len(t) == 0
	}
	return false
}

// readIfPresent loads a config file's declared fields, treating a file
// that is not there as an empty set rather than an error.
func readIfPresent(ctx context.Context, path string) (config.Values, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return config.Values{}, false, nil
		}
		return nil, false, err
	}
	values, err := config.ReadValues(ctx, path)
	if err != nil {
		return nil, false, err
	}
	return values, true, nil
}

// writeValues renders values and writes them to path, creating the
// directory if it is not there yet.
func writeValues(path string, values config.Values, perm os.FileMode) error {
	rendered, err := config.Render(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rendered), perm)
}

// personalSummary is the one line shown instead of asking the personal
// questions again.
func personalSummary(values config.Values) string {
	var parts []string
	for _, field := range wizard.PersonalFields() {
		value, ok := values[field]
		if !ok {
			continue
		}
		if keys, isList := value.([]string); isList {
			parts = append(parts, fmt.Sprintf("%s %s", field, pluralise(len(keys), "entry", "entries")))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %v", field, value))
	}
	if len(parts) == 0 {
		return "none set"
	}
	return strings.Join(parts, ", ")
}

func pluralise(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// printf writes one line of progress.
//
// The error is dropped deliberately: this flow's job is the files it
// writes, and abandoning a half-written config because a status line
// could not reach the terminal would be the worse outcome.
func printf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// merge layers over onto base, returning a new set; neither argument is
// modified.
func merge(base, over config.Values) config.Values {
	merged := config.Values{}
	for field, value := range base {
		merged[field] = value
	}
	for field, value := range over {
		merged[field] = value
	}
	return merged
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
