package lifecycle

import (
	"context"
	"fmt"

	"github.com/jskswamy/cloudlab/internal/reconcile"
)

// RescueSession makes every scrap of a session's work durable on this
// machine: it commits whatever the agent left uncommitted, fetches the
// session branch, and verifies the fetched tip is genuinely in the local
// object store. Returns the local ref the work landed on and the
// instance-side tip that was verified -- a caller that goes on to delete
// anything needs the tip to confirm the session has not moved since (see
// MergeSession).
//
// Idempotent by construction -- the checkpoint is a no-op on a clean tree
// and the fetch moves only missing objects -- so retrying after fixing one
// problem is always safe.
func RescueSession(ctx context.Context, ip, user, localRepo, repoName, session string) (string, string, error) {
	if err := CheckSessionName(session); err != nil {
		return "", "", err
	}
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = client.Close() }()

	repo := RemoteRepoPath(user, session, repoName)
	if out, err := client.Run(checkpointCmd(repo, checkpointMessage(session))); err != nil {
		return "", "", fmt.Errorf("checkpointing work on instance: %w\n%s", err, out)
	}

	branch := SessionBranch(session)
	tip, err := client.Run(remoteGitCmd(repo, "rev-parse", branch))
	if err != nil {
		return "", "", fmt.Errorf("reading session tip on instance: %w\n%s", err, tip)
	}
	tip = trimLine(tip)

	remote := sessionRemote(session)
	if out, err := runLocalGit(ctx, localRepo, fetchRemoteArgs(remote)...); err != nil {
		return "", "", fmt.Errorf("fetching session %s: %w\n%s", session, err, out)
	}
	ref := remote + "/" + branch

	if err := verifyFetched(ctx, localRepo, tip); err != nil {
		return "", "", err
	}
	return ref, tip, nil
}

// checkpointMessage is the subject the instance-side checkpoint commits
// under. Shared so MergeSession's pre-delete re-checkpoint is
// indistinguishable from the rescue's own.
func checkpointMessage(session string) string {
	return "cloudlab: checkpoint " + session
}
