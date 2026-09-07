package beads

import (
	"slices"
	"testing"
)

// TestRemoveRemote_SilentWhenThereIsNoBeadsDatabase proves RemoveRemote is
// inert on the overwhelmingly common case -- a repository that never used
// beads at all -- exactly like Wired. Deliberately runs with no bd on PATH
// requirement: Present's stat-only check must short-circuit before anything
// would need bd, so this must pass even where bd is not installed.
func TestRemoveRemote_SilentWhenThereIsNoBeadsDatabase(t *testing.T) {
	if err := RemoveRemote(t.Context(), t.TempDir(), "fix-auth"); err != nil {
		t.Errorf("RemoveRemote() error = %v, want nil for a repository with no beads database", err)
	}
}

// TestRemoveRemote_ActuallyRemovesARegisteredRemote is what tells
// TestRemoveRemote_SilentWhenThereIsNoBeadsDatabase apart from a stub that
// always returns nil: it proves RemoveRemote does real work when there is
// real work to do.
func TestRemoveRemote_ActuallyRemovesARegisteredRemote(t *testing.T) {
	requireBd(t)
	mac := t.TempDir()
	initGitRepo(t, mac)
	mustRun(t, mac, "bd", "init", "--stealth")

	session := t.TempDir()
	initGitRepo(t, session)
	if err := Seed(t.Context(), mac, "fix-auth", FileURL(session)); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	out, err := run(t.Context(), mac, remoteListArgs()...)
	if err != nil {
		t.Fatalf("dolt remote list before removal: %v\n%s", err, out)
	}
	if !containsRemote(parseRemoteList(out), RemoteName("fix-auth")) {
		t.Fatalf("Seed() did not register %s, fixture setup is broken", RemoteName("fix-auth"))
	}

	if err := RemoveRemote(t.Context(), mac, "fix-auth"); err != nil {
		t.Fatalf("RemoveRemote() error = %v", err)
	}

	out, err = run(t.Context(), mac, remoteListArgs()...)
	if err != nil {
		t.Fatalf("dolt remote list after removal: %v\n%s", err, out)
	}
	if containsRemote(parseRemoteList(out), RemoteName("fix-auth")) {
		t.Errorf("RemoveRemote() left %s registered", RemoteName("fix-auth"))
	}
}

// TestRemoveRemote_SilentWhenThisSessionsRemoteWasNeverRegistered exercises
// the case Fix 2 actually needs: a repository that has beads for other
// sessions, but never had one for this one -- session start with beads =
// "off", say. Wired must key off the specific session's remote, not off
// whether beads exists in the repository at all.
func TestRemoveRemote_SilentWhenThisSessionsRemoteWasNeverRegistered(t *testing.T) {
	requireBd(t)
	mac := t.TempDir()
	initGitRepo(t, mac)
	mustRun(t, mac, "bd", "init", "--stealth")

	if err := RemoveRemote(t.Context(), mac, "never-wired"); err != nil {
		t.Errorf("RemoveRemote() error = %v, want nil when this session's remote was never registered", err)
	}
}

func containsRemote(remotes []Remote, name string) bool {
	for _, r := range remotes {
		if r.Name == name {
			return true
		}
	}
	return false
}

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
