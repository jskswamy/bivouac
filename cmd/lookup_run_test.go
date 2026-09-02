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
