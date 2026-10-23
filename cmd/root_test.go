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
	if !strings.Contains(out.String(), "bivouac") {
		t.Errorf("expected help output to mention bivouac, got: %s", out.String())
	}
}

func TestRootCommandVersionDefault(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--version"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := "bivouac dev (commit: none, built: unknown)\n"
	if out.String() != want {
		t.Fatalf("version output = %q, want %q", out.String(), want)
	}
}

func TestRootCommandVersionInjected(t *testing.T) {
	root := newRootCmdWithBuildInfo(BuildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-10-13T10:00:00Z"})
	root.SetArgs([]string{"--version"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := "bivouac v1.2.3 (commit: abc1234, built: 2026-10-13T10:00:00Z)\n"
	if out.String() != want {
		t.Fatalf("version output = %q, want %q", out.String(), want)
	}
}

// The top level is about the instance. Session verbs -- start, pull, merge --
// live under `session` rather than sitting beside up/down/ssh, so this list
// deliberately does not contain them.
func TestRootCmd_RegistersAllCommands(t *testing.T) {
	want := []string{"connect", "down", "download", "herdr", "image", "init", "list", "manifest", "pair", "preset", "provision", "proxy", "secrets", "serve", "session", "shell", "ssh", "status", "sync", "tailscale", "tmux", "unserve", "up"}
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
