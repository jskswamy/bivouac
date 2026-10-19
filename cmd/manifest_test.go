package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func findTool(t *testing.T, manifest cliManifest, name string) manifestTool {
	t.Helper()
	for _, tool := range manifest.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found in manifest", name)
	return manifestTool{}
}

func TestBuildManifest_DoesNotRunCommands(t *testing.T) {
	root := &cobra.Command{Use: "bivouac"}
	child := &cobra.Command{
		Use:   "explode [name]",
		Short: "must not run",
		RunE: func(cmd *cobra.Command, args []string) error {
			t.Fatal("RunE was invoked during manifest generation")
			return nil
		},
	}
	child.Flags().String("mode", "", "mode to use")
	setCommandMetadata(child, commandMetadata{
		Title:       "Explode test command",
		Interactive: true,
		Network:     true,
		Arguments: []argumentMetadata{{
			Name: "name", Required: false, Description: "optional name",
			Schema: map[string]any{"type": "string"},
		}},
	})
	root.AddCommand(child)

	got := buildManifest(root)
	if got.SchemaVersion != "1" {
		t.Fatalf("SchemaVersion = %q", got.SchemaVersion)
	}
	tool := findTool(t, got, "bivouac_explode")
	if tool.Title != "Explode test command" {
		t.Fatalf("Title = %q", tool.Title)
	}
	if tool.Metadata.Interactive != true || tool.Metadata.Network != true {
		t.Fatalf("metadata = %+v", tool.Metadata)
	}
	if _, ok := tool.InputSchema.Properties["mode"]; !ok {
		t.Fatalf("mode flag missing from input schema")
	}
	if _, ok := tool.InputSchema.Properties["name"]; !ok {
		t.Fatalf("name argument missing from input schema")
	}
}

func TestBuildManifest_IsDeterministic(t *testing.T) {
	root := &cobra.Command{Use: "bivouac", Short: "test root"}
	for _, use := range []string{"zeta", "alpha"} {
		child := &cobra.Command{Use: use, Short: use + " command"}
		child.Flags().String("value", "", "value flag")
		root.AddCommand(child)
	}

	first, err := json.MarshalIndent(buildManifest(root), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.MarshalIndent(buildManifest(root), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("manifest is not deterministic:\nfirst: %s\nsecond: %s", first, second)
	}
}

func manifestFromCommand(t *testing.T, args []string) cliManifest {
	t.Helper()
	cmd := newRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got cliManifest
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	return got
}

func TestManifestCommand_PrintsMCPToolsManifest(t *testing.T) {
	cmd := newRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"manifest", "--format", "mcp-tools"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got cliManifest
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if got.SchemaVersion != "1" || got.Name != "bivouac" {
		t.Fatalf("manifest header = %+v", got)
	}
	herdr := findTool(t, got, "bivouac_herdr")
	if herdr.Title != "Attach Herdr to a bivouac instance/session" {
		t.Fatalf("herdr title = %q", herdr.Title)
	}
	if herdr.Metadata.Interactive != true || herdr.Metadata.Network != true || herdr.Metadata.Destructive != false {
		t.Fatalf("herdr metadata = %+v", herdr.Metadata)
	}
	if herdr.Metadata.JsonOutput {
		t.Fatalf("herdr must not advertise jsonOutput until --json is implemented for it")
	}
	if _, ok := herdr.InputSchema.Properties["name"]; !ok {
		t.Fatalf("herdr input schema missing positional name")
	}
	if _, ok := herdr.InputSchema.Properties["repo"]; !ok {
		t.Fatalf("herdr input schema missing persistent --repo")
	}
}

func TestManifestCommand_IncludesNestedSessionStart(t *testing.T) {
	got := manifestFromCommand(t, []string{"manifest"})
	start := findTool(t, got, "bivouac_session_start")
	if _, ok := start.InputSchema.Properties["name"]; !ok {
		t.Fatalf("session start missing name argument")
	}
	if _, ok := start.InputSchema.Properties["task"]; !ok {
		t.Fatalf("session start missing --task")
	}
	if _, ok := start.InputSchema.Properties["issue"]; !ok {
		t.Fatalf("session start missing --issue")
	}
}

func TestHerdrHelpMentionsLLMRelevantBehavior(t *testing.T) {
	cmd := newRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"herdr", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"instance name, not a session name",
		"outside Herdr",
		"inside Herdr",
		"saved machine",
		"workspace",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("herdr help missing %q:\n%s", want, out)
		}
	}
}

// An LLM drives session merge from the manifest alone, so what it reads there
// has to carry how the command behaves: that a rewritten branch is fine, when
// it refuses, and that a stopped retirement resumes by re-running.
func TestManifest_SessionMergeTellsAnLLMHowItBehaves(t *testing.T) {
	manifest := buildManifest(newRootCmd())

	merge := findTool(t, manifest, "bivouac_session_merge")
	if !merge.Metadata.Destructive || !merge.Metadata.Network || !merge.Metadata.RequiresInstance {
		t.Errorf("session merge metadata = %+v, want destructive, network, requires instance", merge.Metadata)
	}
	for _, want := range []string{"rebas", "garbage collected", "resum", "session delete --force"} {
		if !strings.Contains(merge.Description, want) {
			t.Errorf("session merge description lacks %q:\n%s", want, merge.Description)
		}
	}
}

// An LLM drives session delete from the manifest alone, so what it reads there
// has to carry which refusals --force is for, that a stopped retirement
// resumes by re-running, and that nothing is closed that bivouac cannot prove
// is its own.
func TestManifest_SessionDeleteTellsAnLLMHowItBehaves(t *testing.T) {
	manifest := buildManifest(newRootCmd())

	del := findTool(t, manifest, "bivouac_session_delete")
	if !del.Metadata.Destructive || !del.Metadata.Network || !del.Metadata.RequiresInstance {
		t.Errorf("session delete metadata = %+v, want destructive, network, requires instance", del.Metadata)
	}
	for _, want := range []string{"--force", "cherry-pick", "resum", "retirementPhase", "left open", "cannot prove"} {
		if !strings.Contains(del.Description, want) {
			t.Errorf("session delete description lacks %q:\n%s", want, del.Description)
		}
	}
}
