package cmd

import (
	"bytes"
	"reflect"
	"sort"
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

func TestRootCmd_RegistersAllCommands(t *testing.T) {
	want := []string{"connect", "down", "download", "list", "shell", "ssh", "status", "sync", "up", "watch"}
	root := newRootCmd()
	var got []string
	for _, c := range root.Commands() {
		if c.Name() != "help" && c.Name() != "completion" {
			got = append(got, c.Name())
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("registered commands = %v, want %v", got, want)
	}
}
