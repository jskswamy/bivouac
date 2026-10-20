package lifecycle

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jskswamy/bivouac/internal/state"
)

// The instance name is deliberately different from the recorded path: using
// matching names in fixtures hid the incomplete migration to owner/repo paths.
func TestSessionOperations_UseRecordedRepoPath(t *testing.T) {
	for _, repoName := range []string{"owner/repo", "old-flat-repo", ""} {
		for _, operation := range []string{"pull", "merge", "delete", "force-delete"} {
			t.Run(operation+"/"+repoName, func(t *testing.T) {
				agentCommits := 1
				if operation == "delete" {
					agentCommits = 0
				}
				f := newSessionFixture(t, agentCommits)
				sess := state.Session{Name: f.session, LocalRepo: f.repo, Base: f.base, RepoName: repoName}
				const instance = "instance-name"
				wantRepo := repoName
				if wantRepo == "" {
					wantRepo = instance
				}
				want := RemoteRepoPath("devuser", f.session, wantRepo)
				var mu sync.Mutex
				var commands []string
				f.cmdOverride = func(cmd string) (string, uint32, bool) {
					if strings.Contains(cmd, "/home/devuser/sessions/") {
						mu.Lock()
						commands = append(commands, cmd)
						mu.Unlock()
					}
					return "", 0, false
				}

				var err error
				switch operation {
				case "pull":
					_, _, err = PullSession(context.Background(), f.addr, "devuser", instance, sess)
				case "merge":
					_, err = MergeSession(context.Background(), f.addr, "devuser", instance, sess)
				default:
					var warning string
					warning, err = DeleteSession(context.Background(), f.addr, "devuser", instance, sess, operation == "force-delete")
					if warning != "" {
						t.Errorf("unexpected deletion warning: %s", warning)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(commands) == 0 {
					t.Fatal("no remote repository commands observed")
				}
				for _, cmd := range commands {
					if !strings.Contains(cmd, want) {
						t.Errorf("remote command must use %q, got %s", want, cmd)
					}
				}
				if got, wantRemoved := f.sessionRemoved(), operation != "pull"; got != wantRemoved {
					t.Errorf("repository removed = %v, want %v", got, wantRemoved)
				}
			})
		}
	}
}
