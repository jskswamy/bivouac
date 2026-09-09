package lifecycle

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jskswamy/cloudlab/internal/state"
)

// SessionInfo is what `session list` and `status` display.
//
// A session exists to be a gate the user decides at, so the question these
// two commands have to answer is whether the instance is holding work. That
// cannot be answered from this machine alone: the local branch only moves
// when pull fast-forwards it, so a session worked on since the last pull
// looks identical to one nobody has touched. The instance is asked.
//
// Asked with ls-remote, not fetch: one round trip, no objects transferred,
// nothing written. A listing must stay a read.
type SessionInfo struct {
	Instance string
	Name     string
	Branch   string
	Unmerged int
	// UnmergedKnown reports whether Unmerged was actually computed. A
	// listing tolerates not knowing; a destructive command must not, or a
	// git failure silently reads as "nothing to lose".
	UnmergedKnown bool
	// RemoteKnown reports whether the instance answered at all. False means
	// every other Remote field is meaningless and the display must say so --
	// an unreachable instance is not an empty one, and that distinction is
	// the whole bug this exists for.
	RemoteKnown bool
	// RemoteAhead reports that the instance holds work not on the user's
	// branch. Separate from Unmerged because the two are known independently:
	// ls-remote returns a SHA, so a tip whose object was never fetched is
	// provably ahead and not countable. Ahead-without-a-number is the honest
	// answer there, and it is the one that decides whether to pull.
	RemoteAhead    bool
	WorktreeExists bool
	WorktreeDirty  bool
}

// DescribeSessions describes every session at once, probing their instances
// concurrently.
//
// Concurrent because the probe is a network round trip and the timeout that
// makes it safe is generous: run in sequence, a listing would cost the sum
// of every session's probe, and the worst case would land on whoever has
// the most sessions -- the person the listing is for. Fanned out, the wall
// clock is one probe regardless of how many there are.
//
// Order is preserved, so the display stays stable between runs.
func DescribeSessions(ctx context.Context, instance string, sessions []state.Session) []SessionInfo {
	infos := make([]SessionInfo, len(sessions))
	var wg sync.WaitGroup
	for i, s := range sessions {
		wg.Go(func() {
			infos[i] = DescribeSession(ctx, instance, s)
		})
	}
	wg.Wait()
	return infos
}

// DescribeSession reports a session's local state. Never returns an error:
// a session whose worktree was deleted by hand, or whose repository has gone
// away, should still appear in a listing rather than failing the whole command.
func DescribeSession(ctx context.Context, instance string, s state.Session) SessionInfo {
	info := SessionInfo{
		Instance: instance,
		Name:     s.Name,
		Branch:   SessionBranch(s.Name),
	}

	local := LocalWorktreePath(s.LocalRepo, s.Name)
	if fi, err := os.Stat(local); err == nil && fi.IsDir() {
		info.WorktreeExists = true
		if out, err := runLocalGit(ctx, local, "status", "--porcelain"); err == nil {
			info.WorktreeDirty = trimLine(out) != ""
		}
	}

	describeRemote(ctx, &info, s)
	return info
}

// describeRemote asks the instance where its copy of the session is, and
// works out what can honestly be said about it.
//
// Three outcomes, in the order they are established:
//
//   - The instance did not answer. Fall back to the local branch, which is
//     still the freshest thing here, but leave RemoteKnown false so the
//     display can mark it. Reporting the fallback as the instance's answer
//     is the failure this whole change is about.
//   - It answered with a tip whose object is here, because some earlier
//     fetch brought it. Count exactly, by patch identity, so work already
//     replayed onto the user's branch under new SHAs reads as landed.
//   - It answered with a tip this machine has never seen. It is provably
//     ahead and not countable without pulling; say ahead and no number.
func describeRemote(ctx context.Context, info *SessionInfo, s state.Session) {
	branch := SessionBranch(s.Name)

	tip, err := remoteTip(ctx, s.LocalRepo, sessionRemote(s.Name), branch)
	if err != nil || tip == "" {
		info.Unmerged, info.UnmergedKnown = countUnmerged(ctx, s.LocalRepo, branch)
		info.RemoteAhead = info.UnmergedKnown && info.Unmerged > 0
		return
	}
	info.RemoteKnown = true

	// cat-file -e is the "do we hold this object" question, and asking it
	// is what keeps this a read: the alternative -- fetching so the count
	// always works -- would make a listing mutate the repository.
	if _, err := runLocalGit(ctx, s.LocalRepo, "cat-file", "-e", tip); err != nil {
		info.RemoteAhead = true
		return
	}
	info.Unmerged, info.UnmergedKnown = countUnmerged(ctx, s.LocalRepo, tip)
	info.RemoteAhead = info.UnmergedKnown && info.Unmerged > 0
}

// remoteProbeTimeout bounds the single network call a listing makes.
//
// Both ends of this were measured rather than guessed, and they pull in
// opposite directions. Unbounded, three sessions on an unreachable address
// cost `session list` 2m30s waiting out the OS connect timeout three times.
// Bounded too tightly, a reachable instance gets called unreachable: the
// same probe against a live instance under load average 18 took 11s, and a
// three-second cap would have reported a false negative -- the exact class
// of wrong answer this whole change exists to remove.
//
// Load is the normal case, not the exceptional one: sessions exist because
// agents are working, and agents working is what puts the box under load.
// So the cap is generous and the cost is paid in parallel instead.
const remoteProbeTimeout = 15 * time.Second

// lsRemoteArgs builds the probe.
//
// ConnectTimeout bounds the part of the wait that actually hurt -- the TCP
// connect to a host that is not there -- and BatchMode makes ssh fail
// instead of stopping to ask for a passphrase or a host-key confirmation,
// which in a listing would look exactly like a hang.
func lsRemoteArgs(remote, branch string) []string {
	return []string{
		"-c", "core.sshCommand=ssh -o ConnectTimeout=2 -o BatchMode=yes",
		"ls-remote", "--heads", remote, "refs/heads/" + branch,
	}
}

// remoteTip reads the SHA the instance's session branch points at.
//
// ls-remote rather than fetch: it transfers no objects and writes no refs,
// which is what lets `status` and `session list` ask on every run without
// turning a report into a side effect.
func remoteTip(ctx context.Context, localRepo, remote, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteProbeTimeout)
	defer cancel()

	out, err := runLocalGit(ctx, localRepo, lsRemoteArgs(remote, branch)...)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(trimLine(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// countUnmerged reports how many of rev's commits have no equivalent on HEAD,
// and whether git could answer at all -- a listing tolerates not knowing, a
// destructive command must not.
//
// git cherry, not rev-list --count: after a merge the session's commits exist
// on the branch under different SHAs, and only patch identity sees that they
// landed. A count would report merged work as outstanding. Lines starting "+"
// are the ones with no equivalent upstream.
//
// rev is a parameter rather than always the session branch because the local
// branch and the instance are two different answers: the branch only moves
// when pull fast-forwards it, so delete asks the freshly fetched ref as well.
func countUnmerged(ctx context.Context, localRepo, rev string) (int, bool) {
	out, err := runLocalGit(ctx, localRepo, "cherry", "HEAD", rev)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, line := range nonEmptyLines(out) {
		if strings.HasPrefix(line, "+") {
			n++
		}
	}
	return n, true
}
