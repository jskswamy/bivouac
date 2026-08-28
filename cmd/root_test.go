package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandHelp(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--help"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "cloudlab") {
		t.Errorf("expected help output to mention cloudlab, got: %s", out.String())
	}
}
