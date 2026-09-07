package beads

import (
	"slices"
	"testing"
)

func TestRemoteName_MirrorsTheSessionGitRemote(t *testing.T) {
	// Same name as lifecycle.sessionRemote, so `git remote` and
	// `bd dolt remote list` show the session under one name, not two.
	if got := RemoteName("fix-auth"); got != "cloudlab-fix-auth" {
		t.Errorf("RemoteName() = %q, want %q", got, "cloudlab-fix-auth")
	}
}

func TestRemoteURL_CarriesTheGitPlusSchemeExplicitly(t *testing.T) {
	// The git+ prefix must be written out, never left for bd to infer: with
	// an external sync.remote configured, `bd dolt remote add` appends a bare
	// path to the DoltHub base URL and produces a remote that fails at push.
	got := RemoteURL("subramk", "100.1.2.3", "/home/subramk/sessions/fix-auth/cloudlab")
	want := "git+ssh://subramk@100.1.2.3/home/subramk/sessions/fix-auth/cloudlab"
	if got != want {
		t.Errorf("RemoteURL() = %q, want %q", got, want)
	}
}

func TestFileURL_HasThreeSlashesBeforeAnAbsolutePath(t *testing.T) {
	// git+file://<empty host>/home/... -- the instance bootstraps from its
	// own copy of the repository, locally and offline.
	got := FileURL("/home/subramk/sessions/fix-auth/cloudlab")
	want := "git+file:///home/subramk/sessions/fix-auth/cloudlab"
	if got != want {
		t.Errorf("FileURL() = %q, want %q", got, want)
	}
}

func TestMacSideArgBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"add", addRemoteArgs("cloudlab-x", "git+ssh://u@h/p"),
			[]string{"dolt", "remote", "add", "cloudlab-x", "git+ssh://u@h/p"}},
		{"remove", removeRemoteArgs("cloudlab-x"),
			[]string{"dolt", "remote", "remove", "cloudlab-x"}},
		{"push", pushArgs("cloudlab-x"),
			[]string{"dolt", "push", "--remote", "cloudlab-x"}},
		{"pull", pullArgs("cloudlab-x"),
			[]string{"dolt", "pull", "--remote", "cloudlab-x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}
