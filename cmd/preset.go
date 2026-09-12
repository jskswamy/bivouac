package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/preset"
	"github.com/jskswamy/cloudlab/internal/wizard"
	"github.com/spf13/cobra"
)

// changedLayout is how `preset list` shows a file's mtime: short enough
// for a column, precise enough to tell two edits on the same day apart.
const changedLayout = "2006-01-02 15:04"

func newPresetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "preset",
		Short: "Manage saved project shapes",
		Long: "A preset is a set of answers saved from `cloudlab init` and offered as a\n" +
			"starting point in the next project that has no config. Its settings are\n" +
			"copied into that project's cloudlab.pkl, never linked to from it.",
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(newPresetListCmd(), newPresetShowCmd(), newPresetEditCmd(), newPresetDeleteCmd())
	return c
}

func newPresetListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved presets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			presets, err := preset.List(cmd.Context())
			if err != nil {
				return err
			}
			return printPresets(cmd, presets)
		},
	}
}

// printPresets renders the preset table: what each one is called, what
// it sets, and when it last changed.
//
// "When it changed" is the file's own mtime. Recording when a preset was
// last used would mean writing to it on every read, which is a lot of
// machinery for a column nobody sorts by.
func printPresets(cmd *cobra.Command, presets []preset.Preset) error {
	out := cmd.OutOrStdout()
	if len(presets) == 0 {
		_, err := fmt.Fprintln(out, "no presets — `cloudlab init` offers to save one at the end")
		return err
	}

	s := newStyles(out)
	rows := make([][]string, 0, len(presets))
	for _, p := range presets {
		rows = append(rows, []string{
			s.value.Render(p.Name),
			joinFields(p.Values),
			p.Modified.Format(changedLayout),
		})
	}
	_, err := fmt.Fprintf(out, "\n%s", renderTable(s, []string{"NAME", "SETS", "CHANGED"}, rows, nil))
	return err
}

// joinFields names the fields a preset sets, in the schema's own order
// so that listing twice prints the same line — Values is a map, and its
// iteration order is not.
func joinFields(values config.Values) string {
	var named string
	for _, field := range config.Fields() {
		if _, ok := values[field]; !ok {
			continue
		}
		if named != "" {
			named += ", "
		}
		named += field
	}
	if named == "" {
		return "nothing"
	}
	return named
}

func newPresetShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print a preset's pkl",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			values, err := preset.Load(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			rendered, err := config.Render(values)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), rendered)
			return err
		},
	}
}

func newPresetEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a saved preset, with the same questions that created it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireTerminal(isInteractive()); err != nil {
				return err
			}
			return runPresetEdit(cmd.Context(), cmd.OutOrStdout(), newFormPrompter(), args[0])
		},
	}
}

// runPresetEdit re-asks the project questions with the preset's own
// values filled in, and saves what comes back.
//
// It reuses the init flow's question definitions rather than listing
// them again, so the questions cannot drift between creating a preset
// and editing one.
//
// Fields the project questions do not cover are kept as they were. A
// preset may carry a size override — the one case where a shape implies
// sizing, a toolchain that needs headroom — and editing a preset's
// packages must not quietly drop it. That override is not editable here
// for the same reason the questions are shared: a question set that
// existed only in this command is the drift the design set out to
// avoid.
func runPresetEdit(ctx context.Context, out io.Writer, p prompter, name string) error {
	existing, err := preset.Load(ctx, name)
	if err != nil {
		return err
	}
	values, err := ask(p, wizard.ProjectQuestions(), existing)
	if err != nil {
		return err
	}
	if err := preset.Save(name, values); err != nil {
		return err
	}
	printf(out, "Saved preset %q.\n", name)
	return nil
}

// projectQuestionFields names the fields `preset edit` asks about, in
// order. Exists so the tests can assert that set without restating it.
func projectQuestionFields() []string {
	questions := wizard.ProjectQuestions()
	fields := make([]string, 0, len(questions))
	for _, q := range questions {
		fields = append(fields, q.Field)
	}
	return fields
}

func newPresetDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved preset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// Loaded before asking, so that deleting something that was
			// never there fails as that rather than as a declined prompt.
			if _, err := preset.Load(cmd.Context(), name); err != nil {
				return err
			}
			// No dependency check: presets are stamped into the projects
			// made from them, so nothing can be left pointing at one.
			ok, err := confirm(cmd, fmt.Sprintf("Delete preset %q? Projects already made from it are unaffected. [y/N]: ", name))
			if err != nil {
				return err
			}
			if !ok {
				cmd.Println("Aborted.")
				return nil
			}
			if err := preset.Delete(name); err != nil {
				return err
			}
			cmd.Printf("Deleted preset %q\n", name)
			return nil
		},
	}
}
