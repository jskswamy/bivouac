package lifecycle

import "context"

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
// no cloudlab-specific ref namespace to learn.
func sessionRemote(session string) string {
	return "cloudlab-" + session
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
