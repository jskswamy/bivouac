package cmd

import "github.com/spf13/cobra"

type commandMetadata struct {
	Title            string
	ToolName         string
	Interactive      bool
	Network          bool
	Destructive      bool
	RequiresInstance bool
	RequiresRepo     bool
	MayPrompt        bool
	SupportsJSON     bool
	SideEffects      []string
	Related          []string
	Arguments        []argumentMetadata
	OutputSchema     *jsonSchema
}

type argumentMetadata struct {
	Name        string
	Required    bool
	Description string
	Schema      map[string]any
}

type jsonSchema struct {
	Type                 string         `json:"type,omitempty"`
	Properties           map[string]any `json:"properties,omitempty"`
	Required             []string       `json:"required,omitempty"`
	AdditionalProperties bool           `json:"additionalProperties"`
}

var commandMetadataByCommand = map[*cobra.Command]commandMetadata{}

func setCommandMetadata(cmd *cobra.Command, meta commandMetadata) {
	commandMetadataByCommand[cmd] = meta
}

func metadataFor(cmd *cobra.Command) commandMetadata {
	return commandMetadataByCommand[cmd]
}
