package cmd

import "testing"

func TestDefaultRemoteDir_UsesBasenameUnderHome(t *testing.T) {
	cases := map[string]string{
		"./dataset":        "~/dataset",
		"/abs/path/models":  "~/models",
		"relative/nested":   "~/nested",
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
