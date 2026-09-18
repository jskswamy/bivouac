package lifecycle

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// sshGitURL builds the git URL the Mac uses to reach a repository on the
// instance. An ssh:// URL rather than scp-style user@host:path, because the
// scp form treats a leading slash ambiguously and cannot carry a port later.
//
// OPEN QUESTION (needs one check against a live instance, do not guess):
// git's smart transport runs git-receive-pack / git-upload-pack on the
// instance itself, through sshd and the login shell's -c -- not through the
// `bash -lc` wrapper remoteGitCmd builds. On the instance git comes from
// ~/.nix-profile/bin, cloud-init sets the login shell to /bin/bash, and
// Ubuntu's skel .bashrc returns immediately when non-interactive, so that
// PATH entry may be absent for those two binaries. Whether push and fetch
// work at all may therefore depend on the base image shipping a system
// /usr/bin/git. Check with:
//
//	ssh <instance> 'command -v git-receive-pack git-upload-pack'
//
// If they are absent, the fix is --upload-pack / --receive-pack pointing at
// a `bash -lc` wrapper. Nothing here is changed speculatively.
func sshGitURL(user, ip, path string) string {
	return "ssh://" + user + "@" + ip + path
}

// gitHost picks the address a session's git remote should point at,
// preferring the instance's tailnet address over its public one.
//
// The tailnet address is the better remote for the reason the public one is
// a liability: git traffic then never crosses the public internet, and the
// remote keeps working across a reboot that hands the instance a new public
// IP. It is also the practical fix for repeated auth attempts against port
// 22 on a public address, which is what got an instance blackholed before.
//
// Falls back silently. TailscaleIP returns "" with no error when the daemon
// is absent, logged out, or simply not enabled for this instance, and none
// of those are failures -- an instance without Tailscale is a supported
// configuration, it just gets the public address.
func gitHost(ctx context.Context, ip, user string) string {
	addr, err := TailscaleIP(ctx, ip, user)
	if err != nil || addr == "" {
		return ip
	}
	return addr
}

// pushArgs seeds a session's repository, publishing the Mac's current commit
// as the session branch. src is a committish on this machine (HEAD, or a
// branch name) and session is the branch it lands on over there.
//
// Deliberately never forced: a rejected non-fast-forward means the instance
// holds commits the Mac has not fetched, and overwriting them is precisely
// the data loss this design exists to prevent. Let it fail and let the
// caller explain.
func pushArgs(url, src, sessionBranch string) []string {
	return []string{"push", url, src + ":refs/heads/" + sessionBranch}
}

// sessionRemote is the git remote pointing at a session's repository on the
// instance. A real remote rather than a URL rebuilt per call, so the user
// can run ordinary git against the agent's work -- fetch, log, diff -- with
// no bivouac-specific ref namespace to learn.
func sessionRemote(session string) string {
	return "bivouac-" + session
}

func remoteAddArgs(name, url string) []string {
	return []string{"remote", "add", name, url}
}

func remoteRemoveArgs(name string) []string {
	return []string{"remote", "remove", name}
}

func fetchRemoteArgs(name string) []string {
	return []string{"fetch", "--prune", name}
}

// fastSSHCommand overrides git's ssh transport with a short connect
// timeout, so a fetch or ls-remote against a route that is not there
// fails in seconds instead of waiting out the OS's own TCP connect
// timeout -- unbounded, that cost RescueSession's caller (session
// list, pull, merge, delete) a full minute-plus per session on a
// remote whose stored URL had gone stale. 2s and BatchMode=yes are
// the same measured values sessioninfo.go's ls-remote probe already
// uses, shared here rather than duplicated.
const fastSSHCommand = "core.sshCommand=ssh -o ConnectTimeout=2 -o BatchMode=yes"

// fetchRemoteFastArgs is fetchRemoteArgs bounded by fastSSHCommand.
func fetchRemoteFastArgs(name string) []string {
	return []string{"-c", fastSSHCommand, "fetch", "--prune", name}
}

// probeTimeout bounds a single reachability probe. Shared with
// fastSSHCommand's ConnectTimeout: both measure the same thing -- a
// TCP connect to a host that may not answer -- and 2s is already the
// measured value for that (see fastSSHCommand's comment), so probing
// separately is not given a different, unvalidated number.
const probeTimeout = 2 * time.Second

// dialTarget returns the host:port a probe or an ssh connection
// should dial for gitURL: its own port when the URL names one (as
// this package's tests do, pointed at a fake server on an arbitrary
// port), else ssh's own default, 22 (as sshGitURL's output always
// is for a real instance).
func dialTarget(gitURL string) (string, error) {
	u, err := url.Parse(gitURL)
	if err != nil {
		return "", err
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("no host in %q", gitURL)
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// reachable reports whether target answers a plain TCP connect within
// timeout. No SSH handshake, no git protocol -- proving the port is
// open is enough to decide which address to commit the real fetch
// to, and it is cheaper than finding out by letting that fetch fail.
func reachable(target string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// chooseReachableAddress probes storedURL and fallbackURL at once --
// both are plain TCP dials with nothing to cancel, so racing them
// costs nothing and halves the worst case (one timeout, not the sum
// of two) when both addresses are actually dead.
//
// storedURL failing to parse as a network address (a local filesystem
// path, as some tests use to get a real fetch without a real sshd)
// is reported as reachable, not unreachable: this mechanism exists to
// fall back off a dead network route, not to second-guess a remote
// that was never network-shaped to begin with.
func chooseReachableAddress(storedURL, fallbackURL string, timeout time.Duration) (storedOK, fallbackOK bool) {
	storedTarget, storedErr := dialTarget(storedURL)
	if storedErr != nil {
		return true, false
	}
	fallbackTarget, _ := dialTarget(fallbackURL)

	var wg sync.WaitGroup
	wg.Go(func() { storedOK = reachable(storedTarget, timeout) })
	if fallbackTarget != "" {
		wg.Go(func() { fallbackOK = reachable(fallbackTarget, timeout) })
	}
	wg.Wait()
	return storedOK, fallbackOK
}

// cherryPickSignArgs replays a range onto the checked-out branch, signing
// each commit. Not rebase: the session ref lives outside refs/heads, and
// `git rebase --onto <branch> <branch> <ref>` on a descendant ref reports
// "up to date", re-signs nothing, and detaches HEAD -- leaving the user's
// branch untouched while appearing to succeed.
//
// --empty=drop is what makes a re-run after a conflict work. cherry-pick has
// no patch-id dedup -- that is a rebase behaviour -- so replaying a range
// whose commits already landed stops with "The previous cherry-pick is now
// empty" and leaves the repository mid-cherry-pick. Dropping the ones that
// became empty skips exactly the already-applied ones instead.
func cherryPickSignArgs(revRange string) []string {
	return []string{"cherry-pick", "--empty=drop", "-S", revRange}
}

// countRangeArgs counts the commits in revRange. Used to tell "the agent
// produced nothing" apart from real work: cherry-pick on an empty range
// exits 128, which would make a session that produced nothing, or one
// already merged, impossible to retire.
func countRangeArgs(revRange string) []string {
	return []string{"rev-list", "--count", revRange}
}

// signatureStatusArgs asks git to report each commit's signature state.
// %G? yields G for a good signature and N for none, which is what the
// verification step checks before reporting success.
func signatureStatusArgs(revRange string) []string {
	return []string{"log", "--pretty=%H %G?", revRange}
}
