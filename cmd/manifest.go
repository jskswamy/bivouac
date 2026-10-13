package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type cliManifest struct {
	SchemaVersion string         `json:"schemaVersion"`
	Name          string         `json:"name"`
	Summary       string         `json:"summary,omitempty"`
	Tools         []manifestTool `json:"tools"`
}

type manifestTool struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description,omitempty"`
	InputSchema  jsonSchema       `json:"inputSchema"`
	OutputSchema *jsonSchema      `json:"outputSchema,omitempty"`
	Examples     []string         `json:"examples,omitempty"`
	Metadata     manifestMetadata `json:"metadata"`
}

type manifestMetadata struct {
	CommandPath      []string `json:"commandPath"`
	Use              string   `json:"use,omitempty"`
	Interactive      bool     `json:"interactive"`
	Network          bool     `json:"network"`
	Destructive      bool     `json:"destructive"`
	RequiresInstance bool     `json:"requiresInstance"`
	RequiresRepo     bool     `json:"requiresRepo"`
	MayPrompt        bool     `json:"mayPrompt"`
	JsonOutput       bool     `json:"jsonOutput"`
	SideEffects      []string `json:"sideEffects,omitempty"`
	Related          []string `json:"related,omitempty"`
}

func newManifestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Print machine-readable command metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := cmd.Flags().GetString("format")
			if err != nil {
				return err
			}
			if format != "mcp-tools" {
				return fmt.Errorf("unsupported manifest format %q", format)
			}
			manifest := buildManifest(newRootCmd())
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(manifest)
		},
	}
	cmd.Flags().String("format", "mcp-tools", "manifest format to print")
	setCommandMetadata(cmd, commandMetadata{
		Title:        "Print bivouac's LLM-readable command manifest",
		SupportsJSON: true,
	})
	return cmd
}

func buildManifest(root *cobra.Command) cliManifest {
	manifest := cliManifest{
		SchemaVersion: "1",
		Name:          root.Name(),
		Summary:       root.Short,
	}
	if manifest.Name == "" {
		manifest.Name = strings.Fields(root.Use)[0]
	}
	manifest.Tools = buildManifestTools(root, nil)
	return manifest
}

func buildManifestTools(cmd *cobra.Command, parentPath []string) []manifestTool {
	children := cmd.Commands()
	sort.SliceStable(children, func(i, j int) bool {
		return children[i].Name() < children[j].Name()
	})

	var tools []manifestTool
	for _, child := range children {
		if child.Hidden {
			continue
		}
		path := append(append([]string{}, parentPath...), child.Name())
		tools = append(tools, manifestToolFor(child, path))
		tools = append(tools, buildManifestTools(child, path)...)
	}
	return tools
}

func manifestToolFor(cmd *cobra.Command, path []string) manifestTool {
	meta := metadataFor(cmd)
	name := meta.ToolName
	if name == "" {
		name = "bivouac_" + strings.Join(path, "_")
	}
	title := meta.Title
	if title == "" {
		title = cmd.Short
	}

	return manifestTool{
		Name:         name,
		Title:        title,
		Description:  commandDescription(cmd),
		InputSchema:  inputSchemaForCommand(cmd, meta),
		OutputSchema: meta.OutputSchema,
		Examples:     commandExamples(cmd),
		Metadata: manifestMetadata{
			CommandPath:      append([]string{}, path...),
			Use:              cmd.Use,
			Interactive:      meta.Interactive,
			Network:          meta.Network,
			Destructive:      meta.Destructive,
			RequiresInstance: meta.RequiresInstance,
			RequiresRepo:     meta.RequiresRepo,
			MayPrompt:        meta.MayPrompt,
			JsonOutput:       meta.SupportsJSON,
			SideEffects:      append([]string{}, meta.SideEffects...),
			Related:          append([]string{}, meta.Related...),
		},
	}
}

func commandDescription(cmd *cobra.Command) string {
	if cmd.Long != "" {
		return cmd.Long
	}
	return cmd.Short
}

func commandExamples(cmd *cobra.Command) []string {
	if strings.TrimSpace(cmd.Example) == "" {
		return nil
	}
	lines := strings.Split(cmd.Example, "\n")
	examples := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			examples = append(examples, line)
		}
	}
	return examples
}

func inputSchemaForCommand(cmd *cobra.Command, meta commandMetadata) jsonSchema {
	schema := jsonSchema{
		Type:                 "object",
		Properties:           map[string]any{},
		AdditionalProperties: false,
	}
	for _, arg := range meta.Arguments {
		argSchema := map[string]any{"type": "string"}
		for k, v := range arg.Schema {
			argSchema[k] = v
		}
		if arg.Description != "" {
			argSchema["description"] = arg.Description
		}
		schema.Properties[arg.Name] = argSchema
		if arg.Required {
			schema.Required = append(schema.Required, arg.Name)
		}
	}

	visitFlags := func(flags *pflag.FlagSet) {
		flags.VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || flag.Name == "help" {
				return
			}
			schema.Properties[flag.Name] = schemaForFlag(flag)
		})
	}
	visitFlags(cmd.LocalFlags())
	for parent := cmd.Parent(); parent != nil; parent = parent.Parent() {
		visitFlags(parent.PersistentFlags())
	}
	visitFlags(cmd.InheritedFlags())

	sort.Strings(schema.Required)
	return schema
}

func schemaForFlag(flag *pflag.Flag) map[string]any {
	typeName := "string"
	switch flag.Value.Type() {
	case "bool":
		typeName = "boolean"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		typeName = "integer"
	case "float32", "float64":
		typeName = "number"
	}
	schema := map[string]any{"type": typeName}
	if flag.Usage != "" {
		schema["description"] = flag.Usage
	}
	return schema
}
