package lifecycle

import (
	"context"
	"fmt"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/state"
)

type retirementPolicy struct {
	operation state.RetirementOperation
	force     bool
}

func mergeRetirementPolicy() retirementPolicy {
	return retirementPolicy{operation: state.RetirementMerge}
}

func deleteRetirementPolicy(force bool) retirementPolicy {
	return retirementPolicy{operation: state.RetirementDelete, force: force}
}

func (p retirementPolicy) Check(ctx context.Context, record state.Record, sess state.Session, final bool) (state.Acceptance, []string, error) {
	switch p.operation {
	case state.RetirementMerge:
		return p.checkMerge(ctx, record, sess, final)
	case state.RetirementDelete:
		return p.checkDelete(ctx, record, sess, final)
	default:
		return state.Acceptance{}, nil, fmt.Errorf("unsupported retirement operation %q", p.operation)
	}
}

func (p retirementPolicy) Validate(ctx context.Context, _ state.Record, sess state.Session, accepted state.Acceptance) error {
	if p.operation == state.RetirementDelete && p.force && accepted.SourceTip == "discard-authorised" {
		return nil
	}
	branch, err := runLocalGit(ctx, sess.LocalRepo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("reading retirement destination branch: %w\n%s", err, branch)
	}
	if sess.Retirement != nil && trimLine(branch) != sess.Retirement.Destination.Branch {
		return fmt.Errorf("retirement destination branch changed from %s to %s", sess.Retirement.Destination.Branch, trimLine(branch))
	}
	head, err := HeadCommit(ctx, sess.LocalRepo)
	if err != nil {
		return err
	}
	if head != accepted.VerifiedHead {
		return fmt.Errorf("retirement destination moved from verified head %s to %s", accepted.VerifiedHead, head)
	}
	if p.operation == state.RetirementMerge && accepted.ReplayBase != accepted.VerifiedHead {
		if _, err := verifySignatures(ctx, sess.LocalRepo, accepted.ReplayBase+".."+accepted.VerifiedHead); err != nil {
			return err
		}
	}
	return nil
}

func (p retirementPolicy) checkMerge(ctx context.Context, record state.Record, sess state.Session, _ bool) (state.Acceptance, []string, error) {
	if err := requireBaseExists(ctx, sess.LocalRepo, sess.Base, sess.Name); err != nil {
		return state.Acceptance{}, nil, err
	}
	if err := requireRetirementWorktreesClean(ctx, sess); err != nil {
		return state.Acceptance{}, nil, err
	}
	ref, tip, err := RescueSession(record.ConnectionContext(ctx), record.IP, record.User, sess.LocalRepo, sess.RepoNameOr(record.Name), sess.Name)
	if err != nil {
		return state.Acceptance{}, nil, err
	}
	if err := pullBeadsRequired(record.ConnectionContext(ctx), record, sess); err != nil {
		return state.Acceptance{}, nil, err
	}
	branch, base, err := retirementDestination(ctx, sess.LocalRepo)
	if err != nil {
		return state.Acceptance{}, nil, err
	}
	if sess.Retirement != nil && sess.Retirement.Destination.Branch != "" && sess.Retirement.Destination.Branch != branch {
		return state.Acceptance{}, nil, fmt.Errorf("retirement destination branch changed from %s to %s", sess.Retirement.Destination.Branch, branch)
	}
	revRange := sessionReplayRange(sess.Base, ref)
	if sess.Retirement != nil && len(sess.Retirement.Accepted) > 0 {
		prior := sess.Retirement.Accepted[len(sess.Retirement.Accepted)-1]
		if err := p.Validate(ctx, record, sess, prior); err != nil {
			return state.Acceptance{}, nil, err
		}
		if prior.SourceTip == tip {
			return prior, nil, nil
		}
		if !isAncestor(ctx, sess.LocalRepo, prior.SourceTip, tip) {
			return state.Acceptance{}, nil, fmt.Errorf("session source moved non-monotonically from %s to %s", prior.SourceTip, tip)
		}
		revRange = prior.SourceTip + ".." + ref
	}
	count, err := runLocalGit(ctx, sess.LocalRepo, countRangeArgs(revRange)...)
	if err != nil {
		return state.Acceptance{}, nil, fmt.Errorf("counting session commits: %w\n%s", err, count)
	}
	var signed []string
	replayBase := base
	if trimLine(count) != "0" {
		replayBase, err = replaySignedRange(ctx, sess.LocalRepo, ref, revRange, sess.Name, branch)
		if err != nil {
			return state.Acceptance{}, nil, err
		}
		head, err := HeadCommit(ctx, sess.LocalRepo)
		if err != nil {
			return state.Acceptance{}, nil, err
		}
		if head != replayBase {
			signed, err = verifySignatures(ctx, sess.LocalRepo, replayBase+".."+head)
			if err != nil {
				return state.Acceptance{}, nil, err
			}
		}
	}
	head, err := HeadCommit(ctx, sess.LocalRepo)
	if err != nil {
		return state.Acceptance{}, nil, err
	}
	return state.Acceptance{SourceTip: tip, ReplayBase: replayBase, VerifiedHead: head, IssueEvidence: "pulled"}, signed, nil
}

func (p retirementPolicy) checkDelete(ctx context.Context, record state.Record, sess state.Session, _ bool) (state.Acceptance, []string, error) {
	if p.force {
		head, err := HeadCommit(ctx, sess.LocalRepo)
		if err != nil {
			head = "discard-authorised"
		}
		return state.Acceptance{SourceTip: "discard-authorised", ReplayBase: head, VerifiedHead: head, IssueEvidence: "discard-authorised"}, nil, nil
	}
	if err := requireRetirementWorktreesClean(ctx, sess); err != nil {
		return state.Acceptance{}, nil, err
	}
	ref, tip, err := RescueSession(record.ConnectionContext(ctx), record.IP, record.User, sess.LocalRepo, sess.RepoNameOr(record.Name), sess.Name)
	if err != nil {
		return state.Acceptance{}, nil, fmt.Errorf("cannot confirm what session %s still has on the instance: %w", sess.Name, err)
	}
	for _, rev := range []string{ref, SessionBranch(sess.Name)} {
		unmerged, known := countUnmerged(ctx, sess.LocalRepo, rev)
		if !known {
			return state.Acceptance{}, nil, fmt.Errorf("cannot tell whether session %s has unmerged work", sess.Name)
		}
		if unmerged > 0 {
			return state.Acceptance{}, nil, fmt.Errorf("session %s has %d commit(s) not on your branch", sess.Name, unmerged)
		}
	}
	if err := checkBeadsLanded(record.ConnectionContext(ctx), record.IP, record.User, sess.LocalRepo, RemoteRepoPath(record.User, sess.Name, sess.RepoNameOr(record.Name)), sess.Name); err != nil {
		return state.Acceptance{}, nil, err
	}
	head, err := HeadCommit(ctx, sess.LocalRepo)
	if err != nil {
		return state.Acceptance{}, nil, err
	}
	return state.Acceptance{SourceTip: tip, ReplayBase: head, VerifiedHead: head, IssueEvidence: "landed"}, nil, nil
}

func retirementDestination(ctx context.Context, repo string) (branch, head string, err error) {
	out, err := runLocalGit(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("reading current branch: %w\n%s", err, out)
	}
	branch = trimLine(out)
	head, err = HeadCommit(ctx, repo)
	return branch, head, err
}

func requireRetirementWorktreesClean(ctx context.Context, sess state.Session) error {
	if cherryPickInProgress(ctx, sess.LocalRepo) {
		return fmt.Errorf("a cherry-pick from an earlier session merge is still open")
	}
	status, err := runLocalGit(ctx, sess.LocalRepo, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("checking destination worktree: %w\n%s", err, status)
	}
	if trimLine(status) != "" {
		return fmt.Errorf("your working tree has uncommitted changes (including untracked files)")
	}
	return requireCleanSessionWorktree(ctx, LocalWorktreePath(sess.LocalRepo, sess.Name))
}

func pullBeadsRequired(ctx context.Context, record state.Record, sess state.Session) error {
	if !beads.Wired(ctx, sess.LocalRepo, sess.Name) {
		return nil
	}
	client, err := reconcile.Connect(ctx, record.IP, record.User)
	if err != nil {
		return fmt.Errorf("connecting to pull session %s issue data: %w", sess.Name, err)
	}
	defer func() { _ = client.Close() }()
	if !pullBeads(ctx, client, sess.LocalRepo, RemoteRepoPath(record.User, sess.Name, sess.RepoNameOr(record.Name)), sess.Name) {
		return fmt.Errorf("session %s issue data did not sync; retirement is paused", sess.Name)
	}
	return nil
}
