package cmd

import (
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

func TestDownSummary_WarnsDestructionIsUnrecoverable(t *testing.T) {
	record := state.Record{
		Name: "myrepo", Provider: "digitalocean", Region: "nyc3",
		Size: "s-1vcpu-1gb", Template: "python", IP: "203.0.113.5",
	}

	got := downSummary(record)

	for _, want := range []string{`"myrepo"`, "nyc3", "s-1vcpu-1gb", "python", "203.0.113.5", "cannot be undone", "unsaved work", "Proceed?"} {
		if !strings.Contains(got, want) {
			t.Errorf("downSummary() = %q, want it to contain %q", got, want)
		}
	}
}

func TestDefaultRemoteDir_UsesBasenameUnderHome(t *testing.T) {
	cases := map[string]string{
		"./dataset":        "~/dataset",
		"/abs/path/models": "~/models",
		"relative/nested":  "~/nested",
	}
	for local, want := range cases {
		if got := defaultRemoteDir(local); got != want {
			t.Errorf("defaultRemoteDir(%q) = %q, want %q", local, got, want)
		}
	}
}

func TestDefaultLocalDir_UsesBasenameInCwd(t *testing.T) {
	cases := map[string]string{
		"~/results":       "./results",
		"/root/dataset":   "./dataset",
		"~/nested/output": "./output",
	}
	for remote, want := range cases {
		if got := defaultLocalDir(remote); got != want {
			t.Errorf("defaultLocalDir(%q) = %q, want %q", remote, got, want)
		}
	}
}
