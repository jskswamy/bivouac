package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/state"
)

func TestSessionFromCwd_ReadsTheBranchNotThePath(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)
	wt := filepath.Join(main, ".worktrees", "auth")
	mustGit(t, main, "worktree", "add", "--quiet", wt, "-b", "cloudlab/auth")

	got, ok := SessionFromCwd(context.Background(), wt)
	if !ok || got != "auth" {
		t.Errorf("SessionFromCwd(worktree) = %q, %v; want auth, true", got, ok)
	}

	// The main checkout is on main, not a session branch.
	if got, ok := SessionFromCwd(context.Background(), main); ok {
		t.Errorf("SessionFromCwd(main repo) = %q, %v; want no session", got, ok)
	}
}

func TestResolveSession_PrefersExplicitThenCwdThenOnly(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)
	wt := filepath.Join(main, ".worktrees", "docs")
	mustGit(t, main, "worktree", "add", "--quiet", wt, "-b", "cloudlab/docs")

	var r state.Record
	r.PutSession(state.Session{Name: "auth", Base: "aaa"})
	r.PutSession(state.Session{Name: "docs", Base: "bbb"})

	// 1. explicit wins over everything, including the cwd
	got, err := ResolveSession(context.Background(), wt, r, "auth")
	if err != nil || got.Name != "auth" {
		t.Errorf("explicit = %q, %v; want auth", got.Name, err)
	}

	// 2. cwd wins when there is no argument
	got, err = ResolveSession(context.Background(), wt, r, "")
	if err != nil || got.Name != "docs" {
		t.Errorf("cwd = %q, %v; want docs", got.Name, err)
	}

	// 3. the only session wins when neither applies
	var one state.Record
	one.PutSession(state.Session{Name: "solo"})
	got, err = ResolveSession(context.Background(), main, one, "")
	if err != nil || got.Name != "solo" {
		t.Errorf("single = %q, %v; want solo", got.Name, err)
	}
}

// The ordinary case: cwd and the single session happen to agree. This does
// NOT prove rule 2 runs before rule 3 -- checking "exactly one session"
// first would return the same "auth" here. See
// TestResolveSession_CwdBeatsTheSingleSessionRule below for the case that
// actually distinguishes the two orderings.
func TestResolveSession_CwdMatchesTheOnlySession(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)
	wt := filepath.Join(main, ".worktrees", "auth")
	mustGit(t, main, "worktree", "add", "--quiet", wt, "-b", "cloudlab/auth")

	var r state.Record
	r.PutSession(state.Session{Name: "auth", Base: "aaa"})

	got, err := ResolveSession(context.Background(), wt, r, "")
	if err != nil || got.Name != "auth" {
		t.Errorf("= %q, %v; want auth", got.Name, err)
	}
}

// Cwd is checked before "exactly one session", not merely compatible with
// it: the record's only session is "auth" but cwd sits on cloudlab/docs, a
// name the record does not have. Rule-2-first (correct) sees "docs", finds
// no such session, and fails naming it. Rule-3-first (wrong) would instead
// see exactly one session and silently hand back "auth" -- the wrong
// session, chosen because the cwd was never consulted. The two orderings
// diverge here, which a cwd-matches-the-only-session case can never do.
//
// The failure also has to be a plain error, not *AmbiguousError: the user
// stood in a specific, wrong worktree, so an interactive caller must not
// treat that as "pick one" and offer a picker.
func TestResolveSession_CwdBeatsTheSingleSessionRule(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)
	wt := filepath.Join(main, ".worktrees", "docs")
	mustGit(t, main, "worktree", "add", "--quiet", wt, "-b", "cloudlab/docs")

	var r state.Record
	r.PutSession(state.Session{Name: "auth", Base: "aaa"})

	_, err := ResolveSession(context.Background(), wt, r, "")
	if err == nil {
		t.Fatal("ResolveSession() = nil, want an error naming docs")
	}
	if !strings.Contains(err.Error(), "docs") {
		t.Errorf("error = %q, want it to name docs", err.Error())
	}
	var amb *AmbiguousError
	if errors.As(err, &amb) {
		t.Errorf("error = %v is *AmbiguousError, want a plain error -- the cwd was specific and wrong, not ambiguous", err)
	}
}

func TestResolveSession_AmbiguityNamesTheCandidates(t *testing.T) {
	main := filepath.Join(t.TempDir(), "main")
	initRepo(t, main)

	var r state.Record
	r.PutSession(state.Session{Name: "auth"})
	r.PutSession(state.Session{Name: "docs"})

	_, err := ResolveSession(context.Background(), main, r, "")
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("error = %v, want an *AmbiguousError", err)
	}
	if len(amb.Candidates) != 2 {
		t.Errorf("candidates = %v, want both sessions", amb.Candidates)
	}
	if !strings.Contains(amb.Error(), ".worktrees/") {
		t.Errorf("error = %q, want the cd tip", amb.Error())
	}
}

// Neither case is ambiguity -- one names a session, wrongly; the other has
// no sessions to be ambiguous about -- so both must fail with a plain
// error, never *AmbiguousError, or an interactive caller would wrongly
// offer a picker.
func TestResolveSession_UnknownNameAndNoSessions(t *testing.T) {
	main := filepath.Join(t.TempDir(), "main")
	initRepo(t, main)

	var r state.Record
	r.PutSession(state.Session{Name: "auth"})
	_, err := ResolveSession(context.Background(), main, r, "nope")
	if err == nil {
		t.Fatal("resolving an unknown name succeeded")
	}
	var amb *AmbiguousError
	if errors.As(err, &amb) {
		t.Errorf("error = %v is *AmbiguousError, want a plain error -- an explicit wrong name is not ambiguity", err)
	}

	var empty state.Record
	_, err = ResolveSession(context.Background(), main, empty, "")
	if err == nil {
		t.Fatal("resolving with no sessions succeeded")
	}
	if errors.As(err, &amb) {
		t.Errorf("error = %v is *AmbiguousError, want a plain error -- zero sessions is not ambiguity", err)
	}
}
