package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sessionFixture is a whole session standing up on one machine: a real
// repository playing the agent's clone on the instance, the user's
// repository with the session registered as an ordinary filesystem remote,
// and the local session worktree. Only the genuinely instance-side commands
// -- checkpoint, rev-parse, repo removal -- go through the fake SSH server,
// which is the one thing that cannot be real here.
//
// The remote is a filesystem path rather than an ssh:// URL specifically so
// fetch is a real fetch: rescue's verification, the replay range, and the
// signature gate are then all exercised against real objects.
type sessionFixture struct {
	addr     string
	repo     string // the user's repository
	agent    string // the session's repository on the "instance"
	local    string // the local session worktree merge deletes
	base     string // HEAD at session start, as the state record would hold it
	session  string
	repoName string

	mu       sync.Mutex
	removed  bool
	revParse int
	// onRevParse runs before the nth rev-parse is answered, so a test can
	// move the session underneath the merge.
	onRevParse func(n int)
}

func newSessionFixture(t *testing.T, agentCommits int) *sessionFixture {
	t.Helper()
	startFakeAgent(t)

	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	f := &sessionFixture{
		repo:     filepath.Join(base, "repo"),
		agent:    filepath.Join(base, "agent"),
		session:  "auth",
		repoName: "cloudlab",
	}

	initRepo(t, f.repo)
	configureSigning(t, f.repo, true)
	f.base = gitOut(t, f.repo, "rev-parse", "HEAD")

	// The session's repository, seeded exactly the way session start seeds
	// it: init, push, then checkout. Reversing the last two makes the push
	// illegal, so the order here is part of what the fixture verifies.
	mustGit(t, f.repo, "init", "--quiet", f.agent)
	mustGit(t, f.repo, pushArgs(f.agent, "HEAD", SessionBranch(f.session))...)
	mustGit(t, f.agent, "checkout", "--quiet", SessionBranch(f.session))
	mustGit(t, f.agent, "config", "user.email", "agent@example.com")
	mustGit(t, f.agent, "config", "user.name", "agent")
	for i := 0; i < agentCommits; i++ {
		writeAndCommit(t, f.agent, "agent"+string(rune('a'+i))+".txt", "work", "agent commit")
	}

	mustGit(t, f.repo, remoteAddArgs(sessionRemote(f.session), f.agent)...)
	mustGit(t, f.repo, fetchRemoteArgs(sessionRemote(f.session))...)

	// Session worktrees live inside the repository, so without this the
	// worktree itself reads as untracked content and every cleanliness check
	// in merge fails. session start does exactly this, for the same reason.
	if err := excludeWorktreeDir(context.Background(), f.repo); err != nil {
		t.Fatal(err)
	}
	f.local = LocalWorktreePath(f.repo, f.session)
	mustGit(t, f.repo, "worktree", "add", "--quiet", "--track", "-B", SessionBranch(f.session),
		f.local, sessionRemote(f.session)+"/"+SessionBranch(f.session))

	f.addr = startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		switch {
		case strings.Contains(cmd, "rev-parse"):
			f.mu.Lock()
			f.revParse++
			n := f.revParse
			hook := f.onRevParse
			f.mu.Unlock()
			if hook != nil {
				hook(n)
			}
			return f.tip(t) + "\n", 0
		case strings.Contains(cmd, "rm -rf"):
			f.mu.Lock()
			f.removed = true
			f.mu.Unlock()
			return "", 0
		default: // the checkpoint, which a real clean tree makes a no-op
			return "", 0
		}
	})
	return f
}

func (f *sessionFixture) tip(t *testing.T) string {
	t.Helper()
	return gitOut(t, f.agent, "rev-parse", SessionBranch(f.session))
}

func (f *sessionFixture) sessionRemoved() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed
}

func (f *sessionFixture) pull(t *testing.T) ([]string, error) {
	t.Helper()
	return PullSession(context.Background(), f.addr, "devuser", f.repo, f.repoName, f.session, f.base)
}

func (f *sessionFixture) merge(t *testing.T) ([]string, error) {
	t.Helper()
	return MergeSession(context.Background(), f.addr, "devuser", f.repo, f.repoName, f.session, f.base)
}

// The branch's headline safety property, end to end: every commit is
// replayed onto the user's branch with the user's own signature, git itself
// vouches for each one, and only then does anything get deleted.
func TestMergeSession_ReplaysSignsAndOnlyThenDeletes(t *testing.T) {
	f := newSessionFixture(t, 2)
	before := gitOut(t, f.repo, "rev-parse", "HEAD")

	signed, err := f.merge(t)
	if err != nil {
		t.Fatalf("MergeSession() error = %v", err)
	}
	if len(signed) != 2 {
		t.Fatalf("MergeSession() reported %d signed commits, want 2: %v", len(signed), signed)
	}
	for _, line := range signed {
		if !strings.HasSuffix(line, " G") {
			t.Errorf("reported commit %q is not one git vouches for", line)
		}
	}
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got == before {
		t.Error("the user's branch did not move, but the merge reported success")
	}
	if got := gitOut(t, f.repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("HEAD is on %q after merging, want the user's branch main", got)
	}
	if !f.sessionRemoved() {
		t.Error("the instance-side session was never removed")
	}
	if _, err := os.Stat(f.local); err == nil {
		t.Error("the local session worktree still exists after a successful merge")
	}
	if out, err := runLocalGit(context.Background(), f.repo, "remote", "get-url", sessionRemote(f.session)); err == nil {
		t.Errorf("session remote still registered after merge: %s", out)
	}
}

// Re-running merge after the replay already landed must succeed and retire
// the session, not hard-fail forever.
//
// rev-list --count has no patch-id awareness, so it still counts commits that
// are already on this branch; cherry-pick --empty=drop then correctly drops
// every one of them and leaves HEAD unmoved. Reading that as an empty
// signature range turned the documented conflict-recovery path into a session
// that could only ever be retired by destroying the instance.
func TestMergeSession_CanBeRerunAfterTheReplayAlreadyLanded(t *testing.T) {
	f := newSessionFixture(t, 2)

	if _, err := f.merge(t); err != nil {
		t.Fatalf("first MergeSession() error = %v", err)
	}
	landed := gitOut(t, f.repo, "rev-parse", "HEAD")

	// Put the session back exactly as it was: the state a merge that failed
	// after the replay -- during cleanup, or on the instance -- leaves behind.
	mustGit(t, f.repo, remoteAddArgs(sessionRemote(f.session), f.agent)...)
	mustGit(t, f.repo, fetchRemoteArgs(sessionRemote(f.session))...)
	mustGit(t, f.repo, "worktree", "add", "--quiet", "--track", "-B", SessionBranch(f.session),
		f.local, sessionRemote(f.session)+"/"+SessionBranch(f.session))

	signed, err := f.merge(t)
	if err != nil {
		t.Fatalf("re-run MergeSession() error = %v, want it to retire the session cleanly", err)
	}
	if len(signed) != 0 {
		t.Errorf("re-run reported %d signed commits, want none: everything had already landed", len(signed))
	}
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got != landed {
		t.Errorf("re-run moved the branch from %s to %s, want the already-landed commits replayed once only", landed, got)
	}
	if !f.sessionRemoved() {
		t.Error("the re-run did not retire the instance-side session")
	}
}

// C2, at the level that matters: an unverifiable signature must stop the
// merge *before* anything is deleted, not be reported as success.
func TestMergeSession_DeletesNothingWhenSignaturesDoNotVerify(t *testing.T) {
	f := newSessionFixture(t, 1)
	// Unset the allowed-signers file: the replay still signs, but git can no
	// longer vouch for the result (%G? == N), which is what the gate is for.
	mustGit(t, f.repo, "config", "--unset", "gpg.ssh.allowedSignersFile")

	_, err := f.merge(t)
	if err == nil {
		t.Fatal("MergeSession() = nil, want a refusal when the replayed commits do not verify")
	}
	if f.sessionRemoved() {
		t.Error("the instance-side session was deleted despite the signature check failing")
	}
	if _, statErr := os.Stat(f.local); statErr != nil {
		t.Error("the local session worktree was deleted despite the signature check failing")
	}
}

// A failed verification must rewind the branch, and re-running after one
// must still refuse.
//
// Found on a live instance, not here: the first merge replayed the commits,
// failed to verify them, and correctly deleted nothing -- but left them on
// the branch. The second merge then found them already applied, dropped them
// all as empty, saw an unmoved HEAD, skipped verification on that basis and
// retired the session. A refusal became a silent pass, with unverified
// commits on the user's branch and the session destroyed.
func TestMergeSession_RollsBackAndStaysRefusedAcrossReruns(t *testing.T) {
	f := newSessionFixture(t, 2)
	before := gitOut(t, f.repo, "rev-parse", "HEAD")
	mustGit(t, f.repo, "config", "--unset", "gpg.ssh.allowedSignersFile")

	if _, err := f.merge(t); err == nil {
		t.Fatal("first MergeSession() = nil, want a refusal when the replay cannot be verified")
	}
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got != before {
		t.Fatalf("branch is at %s after a failed verification, want it rewound to %s — unverified commits must not survive on the branch", got, before)
	}

	// The second run is the one that used to pass silently.
	if _, err := f.merge(t); err == nil {
		t.Fatal("re-run MergeSession() = nil, want the same refusal — re-running must not launder unverified commits")
	}
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got != before {
		t.Errorf("branch is at %s after the re-run, want %s", got, before)
	}
	if f.sessionRemoved() {
		t.Error("the session was retired despite never passing verification")
	}
	if _, err := os.Stat(f.local); err != nil {
		t.Error("the local session worktree was deleted despite never passing verification")
	}
}

// I5. An agent that produced nothing, or a session already merged, still has
// to be retirable: cherry-pick on an empty range exits 128.
func TestMergeSession_RetiresASessionWithNoNewCommits(t *testing.T) {
	f := newSessionFixture(t, 0)

	signed, err := f.merge(t)
	if err != nil {
		t.Fatalf("MergeSession() error = %v, want an empty session to retire cleanly", err)
	}
	if len(signed) != 0 {
		t.Errorf("MergeSession() reported %v, want no commits", signed)
	}
	if !f.sessionRemoved() {
		t.Error("an empty session was not removed from the instance")
	}
}

// Rewriting the branch a session was started from must be caught, not
// replayed.
//
// merge computes its range as HEAD..<session ref>, which is only correct while
// the session's base is still an ancestor of HEAD. A rebase, amend, squash or
// re-sign gives every base commit a new SHA, so the old base is unreachable
// and the range silently widens to include a pre-rewrite copy of the entire
// branch history.
//
// Hit for real: a session started from main at 12b52ec, main was then
// re-signed to 84e2047 with identical trees, and HEAD..<ref> went from one
// commit to eight. Nothing warned.
func TestMergeSession_RefusesWhenTheBaseWasRewritten(t *testing.T) {
	f := newSessionFixture(t, 1)

	// Rewrite the branch the session was started from, exactly as an amend or
	// a re-sign would: same tree, new SHA.
	//
	// The message must change. A commit object hashes tree, parents, identity,
	// message and timestamp, so `--amend --no-edit` inside the same second
	// reproduces the object byte for byte and the SHA does not move -- which
	// made this test flaky until the guard below caught it.
	mustGit(t, f.repo, "commit", "--amend", "--allow-empty", "-m", "first, rewritten")
	rewritten := gitOut(t, f.repo, "rev-parse", "HEAD")
	if rewritten == f.base {
		t.Fatal("the amend did not change the SHA; this test would prove nothing")
	}

	_, err := f.merge(t)
	if err == nil {
		t.Fatal("MergeSession() = nil, want a refusal after the base was rewritten")
	}
	if !strings.Contains(err.Error(), f.base) {
		t.Errorf("error = %q, want it to name the recorded base %s", err.Error(), f.base)
	}
	if f.sessionRemoved() {
		t.Error("the session was retired despite the refusal")
	}
}

// The check must not fire on the ordinary case: the branch moving forward
// while a session is open is normal and safe, because the base stays an
// ancestor.
func TestMergeSession_AllowsTheBranchToMoveForward(t *testing.T) {
	f := newSessionFixture(t, 1)
	writeAndCommit(t, f.repo, "unrelated.txt", "meanwhile", "unrelated work on main")

	if _, err := f.merge(t); err != nil {
		t.Fatalf("MergeSession() error = %v, want a fast-moving branch to be fine", err)
	}
	if !f.sessionRemoved() {
		t.Error("the session was not retired")
	}
}

// Attribution trailers the agent wrote must not survive the replay.
//
// cherry-pick preserves the message byte for byte, so whatever the agent put
// in it lands on the user's branch -- and merge then signs it with their key.
// Signing a message you have not seen, crediting an AI you have a standing
// rule against crediting, is the opposite of what the signature asserts.
//
// Seen for real: an agent commit carried Co-Authored-By: Claude and a
// Claude-Session URL, and merge would have reintroduced the exact trailer that
// had just been stripped from the branch by hand.
func TestMergeSession_StripsAgentAttributionTrailers(t *testing.T) {
	f := newSessionFixture(t, 0)

	body := "agent: implement the thing\n" +
		"\n" +
		"Some real detail that must survive.\n" +
		"\n" +
		"Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>\n" +
		"Claude-Session: https://claude.ai/code/session_01G5AauTDFPsccX66qXmCpTA\n"
	if err := os.WriteFile(filepath.Join(f.agent, "thing.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, f.agent, "add", "-A")
	mustGit(t, f.agent, "commit", "--quiet", "-m", body)
	mustGit(t, f.repo, fetchRemoteArgs(sessionRemote(f.session))...)

	if _, err := f.merge(t); err != nil {
		t.Fatalf("MergeSession() error = %v", err)
	}

	got := gitOut(t, f.repo, "log", "-1", "--format=%B")
	for _, banned := range []string{"Co-Authored-By", "anthropic.com", "Claude-Session", "claude.ai"} {
		if strings.Contains(got, banned) {
			t.Errorf("replayed message still contains %q:\n%s", banned, got)
		}
	}
	// Stripping must be surgical -- the real content is the whole point.
	if !strings.Contains(got, "Some real detail that must survive.") {
		t.Errorf("stripping removed real content:\n%s", got)
	}
	if !strings.HasPrefix(got, "agent: implement the thing") {
		t.Errorf("subject was altered:\n%s", got)
	}
}

// A human co-author is legitimate collaboration and must survive.
func TestMergeSession_KeepsHumanCoAuthors(t *testing.T) {
	f := newSessionFixture(t, 0)

	body := "agent: paired work\n\nCo-Authored-By: Alice Smith <alice@example.com>\n"
	if err := os.WriteFile(filepath.Join(f.agent, "paired.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, f.agent, "add", "-A")
	mustGit(t, f.agent, "commit", "--quiet", "-m", body)
	mustGit(t, f.repo, fetchRemoteArgs(sessionRemote(f.session))...)

	if _, err := f.merge(t); err != nil {
		t.Fatalf("MergeSession() error = %v", err)
	}
	if got := gitOut(t, f.repo, "log", "-1", "--format=%B"); !strings.Contains(got, "Alice Smith <alice@example.com>") {
		t.Errorf("a human co-author was stripped:\n%s", got)
	}
}

// An untracked file in the user's repository must not block the merge.
//
// The gate exists because a cherry-pick can fail on a dirty tree, but
// untracked files only collide when a replayed commit adds that same path --
// and git refuses that case on its own. Counting all untracked content made
// merge unusable in any repository with a stray file: cloudlab's own
// cloudlab.pkl is untracked and not gitignored, so cloudlab shipped a file
// that broke its own merge command.
func TestMergeSession_IgnoresUntrackedFilesInTheUsersRepo(t *testing.T) {
	f := newSessionFixture(t, 1)
	if err := os.WriteFile(filepath.Join(f.repo, "cloudlab.pkl"), []byte("region = \"blr1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	signed, err := f.merge(t)
	if err != nil {
		t.Fatalf("MergeSession() error = %v, want an untracked file to be no obstacle", err)
	}
	if len(signed) != 1 {
		t.Errorf("MergeSession() reported %d signed commits, want 1", len(signed))
	}
	if !f.sessionRemoved() {
		t.Error("the session was not retired")
	}
	// The untracked file is the user's; merge must not touch it.
	if _, err := os.Stat(filepath.Join(f.repo, "cloudlab.pkl")); err != nil {
		t.Errorf("merge removed the user's untracked file: %v", err)
	}
}

// A tracked file with uncommitted modifications must still block: that is
// content a cherry-pick can genuinely conflict with.
func TestMergeSession_StillRefusesOnModifiedTrackedFiles(t *testing.T) {
	f := newSessionFixture(t, 1)
	if err := os.WriteFile(filepath.Join(f.repo, "README"), []byte("locally modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := f.merge(t); err == nil {
		t.Fatal("MergeSession() = nil, want a refusal while a tracked file is modified")
	}
	if f.sessionRemoved() {
		t.Error("the session was retired despite the refusal")
	}
}

// I3. pull refuses rather than eat the user's edits in the session worktree;
// merge force-removed the same directory. Both are the same promise.
func TestMergeSession_RefusesToDeleteADirtySessionWorktree(t *testing.T) {
	f := newSessionFixture(t, 1)
	if err := os.WriteFile(filepath.Join(f.local, "mine.txt"), []byte("my edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := f.merge(t)
	if err == nil {
		t.Fatal("MergeSession() = nil, want a refusal while the session worktree holds uncommitted work")
	}
	if !strings.Contains(err.Error(), f.local) {
		t.Errorf("error = %q, want it to name the worktree at %s", err.Error(), f.local)
	}
	if f.sessionRemoved() {
		t.Error("the instance-side session was removed despite the refusal")
	}
	if _, statErr := os.Stat(filepath.Join(f.local, "mine.txt")); statErr != nil {
		t.Error("the user's uncommitted file in the session worktree was destroyed")
	}
}

// I4. The agent keeps working while the replay runs, and signing can block
// for minutes on a hardware key touch. Anything it produced in that window
// is not in the verified fetch, so deleting would destroy it unverified.
func TestMergeSession_AbortsWhenTheSessionMovesDuringTheMerge(t *testing.T) {
	f := newSessionFixture(t, 1)
	// The second rev-parse is the pre-delete re-check; commit on the
	// "instance" just before it answers.
	f.onRevParse = func(n int) {
		if n == 2 {
			writeAndCommit(t, f.agent, "late.txt", "late work", "committed while merging")
		}
	}

	_, err := f.merge(t)
	if err == nil {
		t.Fatal("MergeSession() = nil, want an abort when the session moved after the verified fetch")
	}
	if !strings.Contains(err.Error(), "moved") {
		t.Errorf("error = %q, want it to say the session moved", err.Error())
	}
	if f.sessionRemoved() {
		t.Error("the instance-side session was deleted even though it held unfetched work")
	}
	if _, statErr := os.Stat(f.local); statErr != nil {
		t.Error("the local session worktree was deleted after an aborted merge")
	}
}

// pull is the safe verb: it updates the session worktree and never touches
// the user's branch, however many times it is run.
func TestPullSession_UpdatesTheSessionWorktreeAndNeverTheUsersBranch(t *testing.T) {
	f := newSessionFixture(t, 2)
	userTip := gitOut(t, f.repo, "rev-parse", "HEAD")

	commits, err := PullSession(context.Background(), f.addr, "devuser", f.repo, f.repoName, f.session, f.base)
	if err != nil {
		t.Fatalf("PullSession() error = %v", err)
	}
	if len(commits) != 2 {
		t.Errorf("PullSession() = %v, want the agent's 2 commits", commits)
	}
	if got := gitOut(t, f.repo, "rev-parse", "HEAD"); got != userTip {
		t.Errorf("the user's branch moved from %s to %s -- pull must not touch it", userTip, got)
	}
	if got, want := gitOut(t, f.local, "rev-parse", "HEAD"), f.tip(t); got != want {
		t.Errorf("session worktree is at %s, want it fast-forwarded to %s", got, want)
	}

	// Running it again is a no-op, not an error.
	if _, err := PullSession(context.Background(), f.addr, "devuser", f.repo, f.repoName, f.session, f.base); err != nil {
		t.Fatalf("second PullSession() error = %v, want pull to be repeatable", err)
	}
}

// The named remote is the whole interface: the user runs ordinary git
// against the agent's work, so rescue must land it on a remote-tracking ref
// and not in a bespoke namespace.
func TestRescueSession_LandsWorkOnTheRemoteTrackingRef(t *testing.T) {
	f := newSessionFixture(t, 1)

	ref, tip, err := RescueSession(context.Background(), f.addr, "devuser", f.repo, f.repoName, f.session)
	if err != nil {
		t.Fatalf("RescueSession() error = %v", err)
	}
	want := sessionRemote(f.session) + "/" + SessionBranch(f.session)
	if ref != want {
		t.Errorf("RescueSession() ref = %q, want %q", ref, want)
	}
	if tip != f.tip(t) {
		t.Errorf("RescueSession() tip = %q, want the instance's session tip %q", tip, f.tip(t))
	}
	if got := gitOut(t, f.repo, "rev-parse", ref); got != tip {
		t.Errorf("%s resolves to %s locally, want the verified tip %s", ref, got, tip)
	}
}

// pull listed HEAD..<ref>, the same range merge used before the base check.
// After the base was rewritten that range includes the pre-rewrite copy of the
// branch, so pull over-reported: it named commits the user already had, and
// gave no hint that merge was about to refuse.
func TestPullSession_ReportsOnlyTheSessionsOwnCommitsAfterARewrite(t *testing.T) {
	f := newSessionFixture(t, 1)
	mustGit(t, f.repo, "commit", "--amend", "--allow-empty", "-m", "first, rewritten")

	commits, err := f.pull(t)
	if err != nil {
		t.Fatalf("PullSession() error = %v, want pull to keep working after a rewrite", err)
	}
	if len(commits) != 1 {
		t.Errorf("pull reported %d commits, want only the session's own 1:\n%v", len(commits), commits)
	}
	for _, c := range commits {
		if strings.Contains(c, "first") {
			t.Errorf("pull reported %q, a commit from the pre-rewrite history", c)
		}
	}
}
