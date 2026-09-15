package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jskswamy/cloudlab/internal/beads"
	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/reconcile"
	"github.com/jskswamy/cloudlab/internal/state"
)

// beadsModeFor reads the beads setting out of the repository's cloudlab.pkl.
//
// Best-effort by design. `cloudlab session start` does not require pkl today,
// and beads must not be the reason it starts to: a config that will not
// resolve is a problem `up` and `provision` report properly, with a better
// message than this could give. The fallback is inert on a repository with no
// .beads/, which is the overwhelmingly common case.
func beadsModeFor(ctx context.Context, root string) config.BeadsMode {
	cfg, err := config.Resolve(ctx, filepath.Join(root, "cloudlab.pkl"))
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not resolve "+root+"'s config ("+err.Error()+"); falling back to session mode")
		return config.BeadsSession
	}
	return config.BeadsMode(cfg.Beads)
}

// markIssueStarted claims a --issue session's issue in the session's own
// beads database, so `bd list --status in_progress` finds it on the
// instance without the agent having to remember to say so itself.
//
// beads.Wired is the same check pullBeads and requireBeadsLanded use to
// tell "this session never had beads" apart from a real failure -- and a
// repository with none is a supported configuration, not a reason to skip
// TASK.md, which StartSession has already written by the time this runs.
func markIssueStarted(ctx context.Context, ip, user, localRepo, repo, session, issue string) {
	if !beads.Wired(ctx, localRepo, session) {
		provider.ReportWarning(ctx, "beads: issue "+issue+" cannot be marked in progress -- this session has no issue tracker")
		return
	}
	client, err := reconcile.Connect(ctx, ip, user)
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not connect to mark "+issue+" in progress: "+err.Error())
		return
	}
	defer func() { _ = client.Close() }()
	if err := beads.ClaimIssue(client, repo, issue); err != nil {
		provider.ReportWarning(ctx, "beads: "+err.Error())
	}
}

// runSessionStart backs `cloudlab session start <name>`. Cobra resolves the
// verb now, so there is no hand-rolled dispatch here and no unknown-subcommand
// error to maintain -- an unrecognised verb gets cobra's own suggestion.
func runSessionStart(cmd *cobra.Command, name string, args []string) error {
	// Before any instance work: the --task/--issue conflict TaskText refuses
	// must cost nothing, and resolveInstance below is the first step that
	// touches state or the network.
	task, _ := cmd.Flags().GetString("task")
	issue, _ := cmd.Flags().GetString("issue")
	taskText, err := lifecycle.TaskText(task, issue)
	if err != nil {
		return err
	}

	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	session := args[0]
	// Checked here as well as in StartSession, because the name is persisted
	// before creation is attempted and a name that can never work must not
	// end up in the record.
	if err := lifecycle.CheckSessionName(session); err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoFlag, _ := cmd.Flags().GetString("repo")
	root, err := identity.RepoRoot(ctx, cwd, repoFlag)
	if err != nil {
		return err
	}
	// The commit the session branches from. merge replays HEAD..<session ref>,
	// which is only the session's own work while this stays an ancestor of
	// HEAD -- a later rebase, amend or re-sign of this branch silently widens
	// that range to the whole pre-rewrite history. Captured here because it is
	// the only moment the answer is unambiguous.
	base, err := lifecycle.HeadCommit(ctx, root)
	if err != nil {
		return err
	}

	// All three recorded before the session is created, not after: StartSession
	// makes the branch and repository on the instance before the local fetch
	// that can fail, and a session `down` cannot see is a session `down`
	// destroys. down knows what to rescue only from here -- it resolves an
	// instance by name and may run from anywhere, so none of this is derivable
	// from the caller's working directory.
	// RepoName travels with the session rather than being re-derived from
	// record.Name later: seedSession names the remote directory after
	// whatever instance identity resolved here, and that identity can
	// later stop matching the record's own name -- an instance renamed or
	// consolidated after this session was seeded must not silently point
	// every later pull/merge/delete at a directory that was never seeded.
	record.PutSession(state.Session{Name: session, LocalRepo: root, Base: base, RepoName: name})
	if err := store.Put(record); err != nil {
		return err
	}
	if err := lifecycle.StartSession(ctx, record.IP, record.User, root, name, session, beadsModeFor(ctx, root), taskText); err != nil {
		return err
	}
	if issue != "" {
		markIssueStarted(ctx, record.IP, record.User, root, lifecycle.RemoteRepoPath(record.User, session, name), session, issue)
	}
	cmd.Printf("Session %s started on %s\n", session, name)
	cmd.Printf("\nLocal   %s\n", lifecycle.LocalWorktreePath(root, session))
	cmd.Printf("Fetch   cloudlab session pull %s\n", session)
	cmd.Printf("Accept  cloudlab session merge %s\n", session)
	return nil
}

// runSessionList shows every session on every instance, not just this one:
// the question "what is running anywhere?" is the one this answers, and it is
// unanswerable today without reading state.json by hand.
func runSessionList(cmd *cobra.Command, name string, args []string) error {
	store, err := state.Open()
	if err != nil {
		return err
	}
	records, err := store.List()
	if err != nil {
		return err
	}

	var infos []lifecycle.SessionInfo
	for _, r := range records {
		infos = append(infos, lifecycle.DescribeSessions(cmd.Context(), r.Name, r.Sessions)...)
	}
	if len(infos) == 0 {
		cmd.Println("no sessions")
		return nil
	}

	out := cmd.OutOrStdout()
	s := newStyles(out)
	headers := []string{"NAME", "INSTANCE", "BRANCH", "UNMERGED", "WORKTREE"}
	rows := make([][]string, 0, len(infos))
	for _, i := range infos {
		wtState := "clean"
		if !i.WorktreeExists {
			wtState = "no worktree"
		} else if i.WorktreeDirty {
			wtState = "dirty"
		}
		rows = append(rows, []string{s.value.Render(i.Name), i.Instance, i.Branch, unmergedLabel(i), wtState})
	}
	_, err = fmt.Fprintf(out, "\n%s", renderTable(s, headers, rows, nil))
	return err
}

// resolveSessionArg resolves the session a session-aware command acts on.
// Returns *lifecycle.AmbiguousError unchanged, so an interactive caller can
// offer a picker instead of refusing.
//
// The repository comes back on the session, not from the caller's
// surroundings: ADR-0003 makes two clones of one repo share an instance, so
// the tree you are standing in and the tree the session was started from are
// two different answers, and only the recorded one has the session's
// worktree, remote and branch. down and session delete already read it from
// the record; pull and merge now do too.
//
// cwd is still read, for the "standing in a session worktree names that
// session" rule -- but as a hint about which session, never about which
// repository. Nothing here needs a repo root, so these commands no longer
// refuse to run outside a git repository, which matches down.
// resolveInstanceForSession finds the instance that owns the named session,
// so `session pull/merge/delete <session>` works from wherever it is run --
// the session name given is not fed into cwd-based identity resolution at
// all, so a session on an instance derived from a different repo than the
// one underfoot was unreachable no matter how correct the session name was.
//
// --repo/--name exist to override cwd-based resolution on purpose, so an
// explicit one wins over guessing from the session name.
func resolveInstanceForSession(cmd *cobra.Command, name string, args []string) (*state.Store, state.Record, error) {
	repoFlag, _ := cmd.Flags().GetString("repo")
	nameFlag, _ := cmd.Flags().GetString("name")
	if repoFlag == "" && nameFlag == "" && len(args) > 0 {
		store, err := state.Open()
		if err != nil {
			return nil, state.Record{}, err
		}
		records, err := store.List()
		if err != nil {
			return nil, state.Record{}, err
		}
		var match state.Record
		found := 0
		for _, r := range records {
			if _, ok := r.FindSession(args[0]); ok {
				match = r
				found++
			}
		}
		switch found {
		case 1:
			return store, match, nil
		case 0:
			// Falls through to the cwd-derived lookup below, whose error
			// ("no instance named ...", or ResolveSession's "no session
			// %q") is more specific than a blanket "not found anywhere".
		default:
			return nil, state.Record{}, fmt.Errorf("session %q exists on more than one instance; pass --name to pick one", args[0])
		}
	}
	return resolveInstance(name)
}

func resolveSessionArg(cmd *cobra.Command, record state.Record, args []string) (state.Session, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return state.Session{}, err
	}
	explicit := ""
	if len(args) > 0 {
		explicit = args[0]
	}
	return lifecycle.ResolveSession(cmd.Context(), cwd, record, explicit)
}

func runPull(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	commits, err := lifecycle.PullSession(ctx, record.IP, record.User, sess.LocalRepo, record.Name, sess.Name, sess.Base)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		cmd.Printf("%s: up to date\n", sess.Name)
		return nil
	}
	cmd.Printf("%s: %d new commits\n", sess.Name, len(commits))
	for _, c := range commits {
		cmd.Printf("  %s\n", c)
	}
	cmd.Printf("\nReview  git log HEAD..%s\n", "cloudlab-"+sess.Name+"/"+lifecycle.SessionBranch(sess.Name))
	cmd.Printf("Accept  cloudlab session merge %s\n", sess.Name)
	return nil
}

func runMerge(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	ctx := progressCtx(cmd)
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	signed, err := lifecycle.MergeSession(ctx, record.IP, record.User, record.Name, sess)
	if err != nil {
		return err
	}
	if err := forgetMergedSession(store, record, sess.Name); err != nil {
		return err
	}
	cmd.Printf("%s: %d commits replayed and signed\n", sess.Name, len(signed))
	for _, s := range signed {
		cmd.Printf("  %s\n", s)
	}
	cmd.Printf("session %s removed from both sides\n", sess.Name)
	return nil
}

// forgetMergedSession drops a merged session from the instance's record.
// Leaving it there would make the next `down` try to rescue a session whose
// local remote merge already removed, refuse to destroy, and recommend
// --force -- which skips the rescue for every other session on the instance,
// not just this dead one. A session the record does not name is left alone,
// since the record is the only thing telling `down` what to rescue.
// runSessionDelete drops its entry on the same reasoning.
func forgetMergedSession(store *state.Store, record state.Record, session string) error {
	if _, ok := record.FindSession(session); !ok {
		return nil
	}
	record.RemoveSession(session)
	return store.Put(record)
}

func runSessionDelete(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}
	// The record entry goes whenever the teardown got far enough to return
	// nil, warning or not. DeleteSession removes the local remote on its way
	// out, so an entry left behind after that is one nothing can ever rescue
	// again -- and `down` would refuse on it forever, recommending the --force
	// that skips the rescue for every other session on the instance too.
	warning, err := lifecycle.DeleteSession(cmd.Context(), record.IP, record.User, record.Name, sess, force)
	if err != nil {
		return err
	}
	record.RemoveSession(sess.Name)
	if err := store.Put(record); err != nil {
		return err
	}
	if warning != "" {
		cmd.Println(warning)
	}
	cmd.Printf("session %s deleted\n", sess.Name)
	return nil
}
