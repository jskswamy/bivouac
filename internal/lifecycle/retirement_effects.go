package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/state"
)

var retirementEffectOrder = []string{
	state.EffectRemoteCheckout,
	state.EffectLocalWorktree,
	state.EffectBeadsRemote,
	state.EffectGitRemote,
	state.EffectGitBranch,
	state.EffectReplayJournal,
}

func runRetirementEffects(ctx context.Context, store *state.Store, record state.Record, sess state.Session) error {
	var failures []error
	for _, key := range retirementEffectOrder {
		currentRecord, current, err := reloadRetirement(store, record.Name, sess.Name)
		if err != nil {
			return err
		}
		if current.Retirement.Effects[key].Status == state.EffectDone {
			continue
		}
		if err := intendEffect(store, record.Name, sess.Generation, key); err != nil {
			return err
		}
		if err := applyRetirementEffect(ctx, currentRecord, current, key); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", key, err))
			if !current.Retirement.Force {
				return errors.Join(failures...)
			}
			continue
		}
		if err := updateRetirement(store, record.Name, sess.Generation, func(updated *state.Session) error {
			updated.Retirement.Effects[key] = state.RetirementEffect{Status: state.EffectDone}
			return nil
		}); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

func applyRetirementEffect(ctx context.Context, record state.Record, sess state.Session, key string) error {
	switch key {
	case state.EffectRemoteCheckout:
		return removeRemoteCheckout(ctx, record, sess)
	case state.EffectLocalWorktree:
		return removeLocalWorktree(ctx, sess)
	case state.EffectBeadsRemote:
		return beads.RemoveRemote(ctx, sess.LocalRepo, sess.Name)
	case state.EffectGitRemote:
		return removeSessionGitRemote(ctx, sess)
	case state.EffectGitBranch:
		return removeSessionBranch(ctx, sess)
	case state.EffectReplayJournal:
		path, err := replayProgressPath(ctx, sess.LocalRepo, sess.Name)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown retirement effect %s", key)
	}
}

func removeRemoteCheckout(ctx context.Context, record state.Record, sess state.Session) error {
	client, err := reconcile.Connect(record.ConnectionContext(ctx), record.IP, record.User)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	expected := ""
	if len(sess.Retirement.Accepted) > 0 {
		expected = sess.Retirement.Accepted[len(sess.Retirement.Accepted)-1].SourceTip
	}
	command := removeRepoCheckedCmd(RemoteRepoPath(record.User, sess.Name, sess.RepoNameOr(record.Name)), SessionBranch(sess.Name), expected, sess.Retirement.Force)
	out, err := client.Run(command)
	if err != nil {
		return fmt.Errorf("removing remote checkout: %w\n%s", err, out)
	}
	return nil
}

// removeRepoCheckedCmd makes checkout removal observable and keeps optional
// parent tidying from masking a failed destructive command.
func removeRepoCheckedCmd(repo, branch, expectedTip string, discard bool) string {
	parent := remoteSessionDir(repo)
	var checks string
	if !discard {
		checks = "actual=$(git -C " + shellcmd.Quote(repo) + " rev-parse " + shellcmd.Quote(branch) + ") || exit $?; " +
			"test \"$actual\" = " + shellcmd.Quote(expectedTip) + " || { echo 'session tip moved' >&2; exit 73; }; " +
			"test -z \"$(git -C " + shellcmd.Quote(repo) + " status --porcelain)\" || { echo 'session checkout is dirty' >&2; exit 74; }; "
	}
	inner := "if test ! -e " + shellcmd.Quote(repo) + "; then exit 0; fi; " + checks +
		"rm -rf -- " + shellcmd.Quote(repo) + " || exit $?; " +
		"rm -f -- " + shellcmd.Quote(parent+"/TASK.md") + " || exit $?; " +
		"rmdir -- " + shellcmd.Quote(parent) + " 2>/dev/null || true"
	return shellcmd.LoginShell(inner)
}

func removeLocalWorktree(ctx context.Context, sess state.Session) error {
	local := LocalWorktreePath(sess.LocalRepo, sess.Name)
	if _, err := os.Stat(local); errors.Is(err, os.ErrNotExist) {
		_, _ = runLocalGit(ctx, sess.LocalRepo, "worktree", "prune")
		return nil
	} else if err != nil {
		return err
	}
	if !sess.Retirement.Force {
		if err := requireCleanSessionWorktree(ctx, local); err != nil {
			return err
		}
	}
	args := []string{"worktree", "remove"}
	if sess.Retirement.Force {
		args = append(args, "--force")
	}
	args = append(args, local)
	if out, err := runLocalGit(ctx, sess.LocalRepo, args...); err != nil {
		return fmt.Errorf("removing local worktree %s: %w\n%s", local, err, out)
	}
	_, _ = runLocalGit(ctx, sess.LocalRepo, "worktree", "prune")
	return nil
}

func removeSessionGitRemote(ctx context.Context, sess state.Session) error {
	out, err := runLocalGit(ctx, sess.LocalRepo, "remote")
	if err != nil {
		return err
	}
	name := sessionRemote(sess.Name)
	present := false
	for _, candidate := range nonEmptyLines(out) {
		present = present || candidate == name
	}
	if !present {
		return nil
	}
	out, err = runLocalGit(ctx, sess.LocalRepo, remoteRemoveArgs(name)...)
	if err != nil {
		return fmt.Errorf("removing Git remote %s: %w\n%s", name, err, out)
	}
	return nil
}

func removeSessionBranch(ctx context.Context, sess state.Session) error {
	name := SessionBranch(sess.Name)
	out, err := runLocalGit(ctx, sess.LocalRepo, "branch", "--list", name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	out, err = runLocalGit(ctx, sess.LocalRepo, "branch", "-D", name)
	if err != nil {
		return fmt.Errorf("removing branch %s: %w\n%s", name, err, out)
	}
	return nil
}
