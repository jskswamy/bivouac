package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// sessionTestStore isolates state and returns a store holding record.
func sessionTestStore(t *testing.T, record state.Record) *state.Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	return store
}

func sessionTestCmd() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().String("repo", "", "")
	c.SetOut(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c
}

// A second session on the same instance is the point of this change.
func TestRunSessionStart_AllowsASecondSession(t *testing.T) {
	record := state.Record{Name: "myinstance", IP: "127.0.0.1", User: "devuser"}
	record.PutSession(state.Session{Name: "alpha", LocalRepo: "/repo", Base: "aaa"})
	store := sessionTestStore(t, record)

	// Fails at the connection, not at a guard: reaching the connection is
	// what proves the second session was allowed.
	err := runSessionStart(sessionTestCmd(), "myinstance", []string{"beta"})
	if err != nil && strings.Contains(err.Error(), "already has session") {
		t.Fatalf("a second session was refused: %v", err)
	}
	got, _, _ := store.Get("myinstance")
	if _, ok := got.FindSession("alpha"); !ok {
		t.Error("starting beta removed alpha from the record")
	}
	if _, ok := got.FindSession("beta"); !ok {
		t.Error("beta was not recorded")
	}
}

// A start that fails partway has still created the branch and worktree on
// the instance. If the session name is only recorded on success, `down`
// sees no session at all and destroys the VM without rescuing anything.
func TestRunSession_RecordsTheSessionBeforeCreatingIt(t *testing.T) {
	// Port 1 refuses immediately, so StartSession fails at its first step.
	record := state.Record{Name: "myinstance", IP: "127.0.0.1:1", User: "devuser"}
	store := sessionTestStore(t, record)

	if err := runSessionStart(sessionTestCmd(), "myinstance", []string{"gamma"}); err == nil {
		t.Fatal("runSessionStart() = nil, want the start to fail against a refused connection")
	}
	got, _, _ := store.Get("myinstance")
	sess, ok := got.FindSession("gamma")
	if !ok {
		t.Fatalf("FindSession(gamma) not found after a failed start, want it recorded so down can still rescue it")
	}
	if sess.LocalRepo == "" {
		t.Error("session.LocalRepo is empty after a failed start, so down would not know which repository to fetch into")
	}
}

// TestChooseListener covers every branch of the selection ladder, since a
// prior version of one of them (exactly one listener with no TTY) refused
// with a message that was flatly false: "several ports are listening" over
// a list of one. Error messages are compared exactly, not just presence,
// so a branch drifting back to the wrong wording fails the test that
// should have caught it the first time.
func TestChooseListener(t *testing.T) {
	one := []lifecycle.Listener{{Addr: "127.0.0.1", Port: 8888}}
	many := []lifecycle.Listener{
		{Addr: "127.0.0.1", Port: 8888},
		{Addr: "0.0.0.0", Port: 3000},
	}

	tests := []struct {
		name            string
		listeners       []lifecycle.Listener
		port            int
		discoveryFailed bool
		interactive     bool
		wantListener    lifecycle.Listener
		wantErr         error  // sentinel, checked with errors.Is
		wantErrMsg      string // exact message, checked when non-empty
	}{
		{
			name:            "discovery failed, no port given",
			listeners:       nil,
			port:            0,
			discoveryFailed: true,
			interactive:     true,
			wantErr:         errDiscoveryNoPort,
		},
		{
			name:            "discovery failed, port given forwards blind",
			listeners:       nil,
			port:            8888,
			discoveryFailed: true,
			interactive:     true,
			wantListener:    lifecycle.Listener{Addr: "127.0.0.1", Port: 8888},
		},
		{
			name:         "port matches a listener",
			listeners:    many,
			port:         3000,
			interactive:  true,
			wantListener: lifecycle.Listener{Addr: "0.0.0.0", Port: 3000},
		},
		{
			name:       "port does not match any listener",
			listeners:  many,
			port:       9999,
			wantErrMsg: "nothing is listening on port 9999 — run `cloudlab connect` with no --port to see what is",
		},
		{
			name:      "zero listeners",
			listeners: nil,
			wantErr:   errNoListeners,
		},
		{
			name:         "exactly one listener, no TTY",
			listeners:    one,
			interactive:  false,
			wantListener: one[0],
		},
		{
			name:         "exactly one listener, with a TTY",
			listeners:    one,
			interactive:  true,
			wantListener: one[0],
		},
		{
			name:        "several listeners, no TTY",
			listeners:   many,
			interactive: false,
			wantErrMsg:  "several ports are listening; pass --port (no terminal to ask on)",
		},
		{
			name:        "several listeners, with a TTY asks the user",
			listeners:   many,
			interactive: true,
			wantErr:     errAskUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseListener(tt.listeners, tt.port, tt.discoveryFailed, tt.interactive)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("chooseListener() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if tt.wantErrMsg != "" {
				if err == nil || err.Error() != tt.wantErrMsg {
					t.Fatalf("chooseListener() err = %v, want message %q", err, tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseListener() unexpected err = %v", err)
			}
			if got != tt.wantListener {
				t.Fatalf("chooseListener() = %+v, want %+v", got, tt.wantListener)
			}
		})
	}
}

// Retrying the same session is not a second session -- it is how a failed
// start is fixed.
func TestRunSession_AllowsRetryingTheSameSession(t *testing.T) {
	record := state.Record{Name: "myinstance", IP: "127.0.0.1:1", User: "devuser"}
	record.PutSession(state.Session{Name: "gamma"})
	sessionTestStore(t, record)

	err := runSessionStart(sessionTestCmd(), "myinstance", []string{"gamma"})
	if err == nil {
		t.Fatal("runSessionStart() = nil, want it to get past the guard and fail at the connection")
	}
	if strings.Contains(err.Error(), "already has session") {
		t.Errorf("error = %q, want a retry of the same session to be allowed", err.Error())
	}
}

func TestRunSession_RejectsANameThatCannotBeABranchOrPath(t *testing.T) {
	record := state.Record{Name: "myinstance", IP: "127.0.0.1:1", User: "devuser"}
	store := sessionTestStore(t, record)

	if err := runSessionStart(sessionTestCmd(), "myinstance", []string{"../escape"}); err == nil {
		t.Fatal("runSessionStart() = nil, want a bad session name refused")
	}
	got, _, _ := store.Get("myinstance")
	if len(got.Sessions) != 0 {
		t.Errorf("Sessions = %+v, want a name that can never work not to be persisted", got.Sessions)
	}
}

// A forced delete against a dead instance tears the local half down -- remote
// included -- so the record entry must go with it. An entry left behind names
// a session whose local remote no longer exists, which nothing can ever rescue
// again: every later `cloudlab down` refuses on it and recommends the --force
// that skips the rescue for every other session on the instance too.
func TestRunSessionDelete_DropsTheRecordWhenOnlyTheInstanceHalfSurvives(t *testing.T) {
	localRepo := t.TempDir()
	mustGitCmd(t, localRepo, "init", "--quiet")

	// Port 1 refuses immediately: the instance side cannot be reached, which
	// is the case that used to return an error and strand the entry.
	record := state.Record{Name: "myinstance", IP: "127.0.0.1:1", User: "devuser"}
	record.PutSession(state.Session{Name: "auth", LocalRepo: localRepo})
	store := sessionTestStore(t, record)

	cmd := sessionTestCmd()
	cmd.Flags().Bool("force", false, "")
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	// --repo pins resolution to the throwaway repository, so nothing here can
	// reach the repository the test itself is running inside.
	if err := cmd.Flags().Set("repo", localRepo); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	cmd.SetOut(out)

	if err := runSessionDelete(cmd, "myinstance", []string{"auth"}); err != nil {
		t.Fatalf("runSessionDelete() error = %v, want the unreachable instance reported as a warning, not an error", err)
	}
	got, _, _ := store.Get("myinstance")
	if _, ok := got.FindSession("auth"); ok {
		t.Error("the session survived in the record after its local remote was torn down -- nothing could ever rescue it again")
	}
	if !strings.Contains(out.String(), "/home/devuser/sessions/auth/") {
		t.Errorf("output = %q, want it to name the directory left behind on the instance", out.String())
	}
}

// After a successful merge the session exists on neither machine. A record
// that still names it makes the next `down` refuse to destroy and recommend
// --force, which is the one path that loses work.
func TestForgetMergedSession_ClearsTheRecordSoDownStopsRescuing(t *testing.T) {
	record := state.Record{Name: "myinstance"}
	record.PutSession(state.Session{Name: "alpha", LocalRepo: "/some/repo"})
	store := sessionTestStore(t, record)

	if err := forgetMergedSession(store, record, "alpha"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get("myinstance")
	if len(got.Sessions) != 0 {
		t.Errorf("Sessions after merge = %+v, want cleared", got.Sessions)
	}
}

func TestForgetMergedSession_LeavesADifferentSessionAlone(t *testing.T) {
	record := state.Record{Name: "myinstance"}
	record.PutSession(state.Session{Name: "alpha", LocalRepo: "/some/repo"})
	store := sessionTestStore(t, record)

	if err := forgetMergedSession(store, record, "beta"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get("myinstance")
	if _, ok := got.FindSession("alpha"); !ok {
		t.Error("FindSession(alpha) not found, want the live session still recorded for down to rescue")
	}
}

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

func TestSyncLocalDir_UsesDirFlagOrCwd(t *testing.T) {
	if got, err := syncLocalDir("/explicit/path"); err != nil || got != "/explicit/path" {
		t.Errorf("syncLocalDir(%q) = (%q, %v), want (%q, nil)", "/explicit/path", got, err, "/explicit/path")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := syncLocalDir(""); err != nil || got != cwd {
		t.Errorf("syncLocalDir(\"\") = (%q, %v), want (%q, nil)", got, err, cwd)
	}
}

func TestSyncRemoteDir_UsesFirstArgOrRemotePath(t *testing.T) {
	if got, err := syncRemoteDir([]string{"~/custom"}, "/whatever", "devuser"); err != nil || got != "~/custom" {
		t.Errorf("syncRemoteDir([\"~/custom\"], ...) = (%q, %v), want (%q, nil)", got, err, "~/custom")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "project")
	got, err := syncRemoteDir(nil, local, "devuser")
	if err != nil {
		t.Fatalf("syncRemoteDir(nil, ...) error = %v", err)
	}
	want := "/home/devuser/project"
	if got != want {
		t.Errorf("syncRemoteDir(nil, ...) = %q, want %q", got, want)
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

func TestTmuxSession_UsesFirstArgOrDefault(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "main"},
		{[]string{"myrepo"}, "myrepo"},
	}
	for _, tc := range cases {
		if got := tmuxSession(tc.args); got != tc.want {
			t.Errorf("tmuxSession(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// With one session the name is ceremony: the record already knows it.
func TestRunPull_ResolvesTheOnlySessionWithoutAnArgument(t *testing.T) {
	repo := t.TempDir()
	mustGitCmd(t, repo, "init", "--quiet")

	record := state.Record{Name: "myinstance", IP: "127.0.0.1", User: "devuser"}
	record.PutSession(state.Session{Name: "solo", LocalRepo: repo, Base: "aaa"})
	sessionTestStore(t, record)

	c := sessionTestCmd()
	c.SetArgs(nil)
	err := runPull(c, "myinstance", nil)
	// It will fail at the connection; what matters is that it did not fail
	// for want of an argument.
	if err != nil && strings.Contains(err.Error(), "no sessions") {
		t.Fatalf("pull did not resolve the only session: %v", err)
	}
}

func TestRunPull_AmbiguityRefusesRatherThanPrompting(t *testing.T) {
	repo := t.TempDir()
	mustGitCmd(t, repo, "init", "--quiet")

	record := state.Record{Name: "myinstance", IP: "127.0.0.1", User: "devuser"}
	record.PutSession(state.Session{Name: "auth", LocalRepo: repo})
	record.PutSession(state.Session{Name: "docs", LocalRepo: repo})
	sessionTestStore(t, record)

	err := runPull(sessionTestCmd(), "myinstance", nil)
	if err == nil {
		t.Fatal("runPull() = nil with two sessions and no argument, want a refusal")
	}
	if !strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), "docs") {
		t.Errorf("error = %q, want both candidates named", err.Error())
	}
}

func mustGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The empty state file is the common case on a fresh machine, and it must
// read as "nothing running" rather than an error.
func TestRunSessionList_NoSessions_PrintsNoSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	c := sessionTestCmd()
	var out bytes.Buffer
	c.SetOut(&out)

	if err := runSessionList(c, "", nil); err != nil {
		t.Fatalf("runSessionList() error = %v", err)
	}
	if !strings.Contains(out.String(), "no sessions") {
		t.Errorf("output = %q, want it to report no sessions", out.String())
	}
}

// The whole point of `session list` is seeing sessions on every instance in
// one place, so it has to walk every record, not just the first.
func TestRunSessionList_AggregatesAcrossRecords(t *testing.T) {
	repo := t.TempDir()
	mustGitCmd(t, repo, "init", "--quiet")

	one := state.Record{Name: "one", IP: "127.0.0.1"}
	one.PutSession(state.Session{Name: "alpha", LocalRepo: repo})
	store := sessionTestStore(t, one)

	two := state.Record{Name: "two", IP: "127.0.0.1"}
	two.PutSession(state.Session{Name: "beta", LocalRepo: repo})
	if err := store.Put(two); err != nil {
		t.Fatal(err)
	}

	c := sessionTestCmd()
	var out bytes.Buffer
	c.SetOut(&out)

	if err := runSessionList(c, "", nil); err != nil {
		t.Fatalf("runSessionList() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "one") {
		t.Errorf("output = %q, want it to list session alpha on instance one", got)
	}
	if !strings.Contains(got, "beta") || !strings.Contains(got, "two") {
		t.Errorf("output = %q, want it to list session beta on instance two", got)
	}
	if !strings.Contains(got, "no worktree") {
		t.Errorf("output = %q, want both sessions reported as having no worktree (never started)", got)
	}
}

// status answers "what is on this one?" -- every session on the instance,
// each with enough state to say whether it is safe to walk away from.
func TestPrintSessions_ListsEverySessionWithItsState(t *testing.T) {
	repo := t.TempDir()
	mustGitCmd(t, repo, "init", "--quiet")

	record := state.Record{Name: "myinstance"}
	record.PutSession(state.Session{Name: "auth", LocalRepo: repo})
	record.PutSession(state.Session{Name: "docs", LocalRepo: repo})

	c := sessionTestCmd()
	var out bytes.Buffer
	c.SetOut(&out)

	printSessions(c, record)

	for _, want := range []string{"auth", "docs", "cloudlab/auth", "cloudlab/docs"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not mention %q:\n%s", want, out.String())
		}
	}
}

// A fresh instance has no sessions, and that must read as "none" rather
// than an empty, ambiguous section.
func TestPrintSessions_SaysNoneWhenThereAreNone(t *testing.T) {
	c := sessionTestCmd()
	var out bytes.Buffer
	c.SetOut(&out)

	printSessions(c, state.Record{Name: "myinstance"})

	if !strings.Contains(out.String(), "none") {
		t.Errorf("output = %q, want it to say none", out.String())
	}
}

// With no tailnet address there is nothing to choose, so pair must not
// stop to ask -- the public IP is the only answer.
func TestChoosePairHost_NoTailscale_ReturnsPublicWithoutPrompting(t *testing.T) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(strings.NewReader(""))

	got, err := choosePairHost(c, state.Record{IP: "203.0.113.5", TailscaleJoined: false})
	if err != nil {
		t.Fatalf("choosePairHost() error = %v", err)
	}
	if got != "203.0.113.5" {
		t.Errorf("choosePairHost() = %q, want the public IP", got)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q, want no prompt when there is nothing to choose", out.String())
	}
}

// ADR-0003 makes two clones of one repo share an instance, so the repo a
// session belongs to and the repo you are standing in are genuinely two
// different answers. The session's own is the right one: its worktree, its
// remote and its branch all live there, and merge replays onto its branch.
//
// Asserted through the dirty-tree gate because that is the last thing merge
// does before it connects: dirtying only the session's repo makes a refusal
// proof that repo was the one inspected. Standing in a clean clone and
// reaching the connection instead is the bug.
func TestRunMerge_OperatesOnTheSessionsRepoNotTheCwd(t *testing.T) {
	session := newTestRepo(t)
	mustGitCmd(t, session, "commit", "--allow-empty", "--quiet", "-m", "base")
	base := headOf(t, session)

	// A real clone, so the session's base is reachable from both and the
	// only thing separating them is which one merge chose to inspect.
	elsewhere := filepath.Join(t.TempDir(), "clone")
	mustGitCmd(t, session, "clone", "--quiet", session, elsewhere)

	if err := os.WriteFile(filepath.Join(session, "tracked.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	record := state.Record{Name: "myinstance", IP: "127.0.0.1", User: "devuser"}
	record.PutSession(state.Session{Name: "solo", LocalRepo: session, Base: base})
	sessionTestStore(t, record)

	t.Chdir(elsewhere)
	err := runMerge(sessionTestCmd(), "myinstance", nil)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("runMerge() error = %v, want a refusal naming the session repo's uncommitted changes", err)
	}
}

// newTestRepo is a git repository that can commit: identity configured, and
// one tracked file for a test to dirty.
func newTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustGitCmd(t, repo, "init", "--quiet")
	mustGitCmd(t, repo, "config", "user.email", "t@example.com")
	mustGitCmd(t, repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGitCmd(t, repo, "add", "tracked.txt")
	mustGitCmd(t, repo, "commit", "--quiet", "-m", "tracked")
	return repo
}

func TestPickListener_ValidChoice(t *testing.T) {
	listeners := []lifecycle.Listener{
		{Addr: "0.0.0.0", Port: 8888, Process: "python3.12"},
		{Addr: "127.0.0.1", Port: 3000, Process: "node"},
	}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("2\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)

	got, err := pickListener(cmd, listeners)
	if err != nil {
		t.Fatalf("pickListener() error = %v", err)
	}
	if got.Port != 3000 {
		t.Errorf("pickListener() = port %d, want 3000", got.Port)
	}
	if !strings.Contains(out.String(), "8888") || !strings.Contains(out.String(), "node") {
		t.Errorf("pickListener() did not list the candidates:\n%s", out.String())
	}
}

func TestPickListener_OutOfRange(t *testing.T) {
	listeners := []lifecycle.Listener{{Addr: "0.0.0.0", Port: 8888}}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("7\n"))
	cmd.SetOut(&bytes.Buffer{})

	if _, err := pickListener(cmd, listeners); err == nil {
		t.Error("pickListener() error = nil, want an error for a choice outside 1-1")
	}
}

func TestServeTarget_RoutableNeedsNoEntry(t *testing.T) {
	// Already reachable at the tailnet address, so a serve entry would
	// be clutter someone has to clean up later.
	url, needsEntry := serveTarget("100.81.106.84", lifecycle.Listener{Addr: "0.0.0.0", Port: 3000})
	if needsEntry {
		t.Error("needsEntry = true, want false for a routable service")
	}
	if url != "http://100.81.106.84:3000" {
		t.Errorf("url = %q, want the tailnet URL", url)
	}
}

func TestServeTarget_LoopbackNeedsAnEntry(t *testing.T) {
	url, needsEntry := serveTarget("100.81.106.84", lifecycle.Listener{Addr: "127.0.0.1", Port: 8888})
	if !needsEntry {
		t.Error("needsEntry = false, want true — loopback is not reachable on the tailnet")
	}
	if url != "http://100.81.106.84:8888" {
		t.Errorf("url = %q, want the tailnet URL it will be reachable at once served", url)
	}
}

func TestServeTarget_NonHTTPPortGetsNoScheme(t *testing.T) {
	url, _ := serveTarget("100.81.106.84", lifecycle.Listener{Addr: "127.0.0.1", Port: 22})
	if url != "100.81.106.84:22" {
		t.Errorf("url = %q, want no http:// on a port that does not speak HTTP", url)
	}
}

func headOf(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
