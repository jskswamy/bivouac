package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/beads"
	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/identity"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/state"
)

// beadsModeFor reads the beads setting out of the repository's bivouac.pkl.
//
// Best-effort by design. `bivouac session start` does not require pkl today,
// and beads must not be the reason it starts to: a config that will not
// resolve is a problem `up` and `provision` report properly, with a better
// message than this could give. The fallback is inert on a repository with no
// .beads/, which is the overwhelmingly common case.
func beadsModeFor(ctx context.Context, root string) config.BeadsMode {
	cfg, err := config.Resolve(ctx, filepath.Join(root, "bivouac.pkl"))
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

// runSessionStart backs `bivouac session start <name>`. Cobra resolves the
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

	store, record, err := resolveInstance(cmd, name)
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
	instanceRelease, err := store.LockInstance(cmd.Context(), record.Name, false)
	if err != nil {
		return err
	}
	defer func() { _ = instanceRelease() }()
	sessionRelease, err := store.LockSession(cmd.Context(), record.Name, session)
	if err != nil {
		return err
	}
	defer func() { _ = sessionRelease() }()
	if latest, ok, err := store.Get(record.Name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("instance %s disappeared while waiting to start session %s", record.Name, session)
	} else {
		record = latest
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

	repoPath, err := identity.DeriveRepoPath(ctx, root)
	if err != nil {
		return err
	}
	if err := lifecycle.CheckRemoteRepoPath(repoPath); err != nil {
		return err
	}

	// All three recorded before the session is created, not after: StartSession
	// makes the branch and repository on the instance before the local fetch
	// that can fail, and a session `down` cannot see is a session `down`
	// destroys. down knows what to rescue only from here -- it resolves an
	// instance by name and may run from anywhere, so none of this is derivable
	// from the caller's working directory.
	// RepoName travels with the session rather than being re-derived from
	// record.Name later: seedSession names the remote directory after the
	// repository identity resolved here, and that identity can later stop
	// matching the record's own name -- an instance renamed or consolidated
	// after this session was seeded must not silently point every later
	// pull/merge/delete at a directory that was never seeded.
	if err := store.Update(record.Name, func(latest *state.Record) error {
		_, err := latest.PrepareSessionStart(session, root, base, repoPath)
		return err
	}); err != nil {
		return err
	}
	if err := lifecycle.StartSession(ctx, record.IP, record.User, root, repoPath, session, beadsModeFor(ctx, root), taskText); err != nil {
		return err
	}
	if issue != "" {
		markIssueStarted(ctx, record.IP, record.User, root, lifecycle.RemoteRepoPath(record.User, session, repoPath), session, issue)
	}
	cmd.Printf("Session %s started on %s\n", session, name)
	cmd.Printf("\nLocal   %s\n", lifecycle.LocalWorktreePath(root, session))
	cmd.Printf("Fetch   bivouac session pull %s\n", session)
	cmd.Printf("Accept  bivouac session merge %s\n", session)
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
		infos = append(infos, lifecycle.DescribeSessions(r.ConnectionContext(cmd.Context()), r.Name, r.Sessions)...)
	}
	if jsonFlag(cmd) {
		return printSessionListJSON(cmd, infos)
	}
	if len(infos) == 0 {
		cmd.Println("no sessions")
		return nil
	}

	out := cmd.OutOrStdout()
	s := newStyles(out)
	headers := []string{"NAME", "INSTANCE", "BRANCH", "UNMERGED", "WORKTREE", "STATE"}
	rows := make([][]string, 0, len(infos))
	for _, i := range infos {
		wtState := "clean"
		if !i.WorktreeExists {
			wtState = "no worktree"
		} else if i.WorktreeDirty {
			wtState = "dirty"
		}
		rows = append(rows, []string{s.value.Render(i.Name), i.Instance, i.Branch, unmergedLabel(i), wtState, retirementLabel(i)})
	}
	_, err = fmt.Fprintf(out, "\n%s", renderTable(s, headers, rows, nil))
	return err
}

func printSessionListJSON(cmd *cobra.Command, infos []lifecycle.SessionInfo) error {
	type sessionJSON struct {
		Name            string `json:"name"`
		Instance        string `json:"instance"`
		Branch          string `json:"branch"`
		WorktreeExists  bool   `json:"worktreeExists"`
		WorktreeDirty   bool   `json:"worktreeDirty"`
		RemoteKnown     bool   `json:"remoteKnown"`
		UnmergedKnown   bool   `json:"unmergedKnown"`
		Unmerged        int    `json:"unmerged"`
		RemoteAhead     bool   `json:"remoteAhead"`
		Operation       string `json:"operation,omitempty"`
		RetirementPhase string `json:"retirementPhase,omitempty"`
		LastFailure     string `json:"lastFailure,omitempty"`
	}
	out := struct {
		Sessions []sessionJSON `json:"sessions"`
	}{Sessions: []sessionJSON{}}
	for _, info := range infos {
		out.Sessions = append(out.Sessions, sessionJSON{
			Name:            info.Name,
			Instance:        info.Instance,
			Branch:          info.Branch,
			WorktreeExists:  info.WorktreeExists,
			WorktreeDirty:   info.WorktreeDirty,
			RemoteKnown:     info.RemoteKnown,
			UnmergedKnown:   info.UnmergedKnown,
			Unmerged:        info.Unmerged,
			RemoteAhead:     info.RemoteAhead,
			Operation:       info.Operation,
			RetirementPhase: info.RetirementPhase,
			LastFailure:     info.LastFailure,
		})
	}
	return writeJSON(cmd, out)
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
			matched := false
			if _, ok := r.FindSession(args[0]); ok {
				matched = true
			}
			if !matched {
				for _, receipt := range r.RetirementReceipts {
					if receipt.Session == args[0] {
						matched = true
						break
					}
				}
			}
			if matched {
				match = r
				found++
			}
		}
		switch found {
		case 1:
			cmd.SetContext(match.ConnectionContext(cmd.Context()))
			return store, match, nil
		case 0:
			// Falls through to the cwd-derived lookup below, whose error
			// ("no instance named ...", or ResolveSession's "no session
			// %q") is more specific than a blanket "not found anywhere".
		default:
			return nil, state.Record{}, fmt.Errorf("session %q exists on more than one instance; pass --name to pick one", args[0])
		}
	}
	return resolveInstance(cmd, name)
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
	commits, err := lifecycle.PullSession(ctx, record.IP, record.User, record.Name, sess)
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
	cmd.Printf("\nReview  git log HEAD..%s\n", "bivouac-"+sess.Name+"/"+lifecycle.SessionBranch(sess.Name))
	cmd.Printf("Accept  bivouac session merge %s\n", sess.Name)
	return nil
}

func runMerge(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	session := ""
	if len(args) > 0 {
		session = args[0]
	} else {
		sess, err := resolveSessionArg(cmd, record, args)
		if err != nil {
			return err
		}
		session = sess.Name
	}
	result, err := lifecycle.RetireSession(progressCtx(cmd), store, record.Name, session, lifecycle.RetirementOptions{Operation: state.RetirementMerge})
	if err != nil {
		return err
	}
	if result.AlreadyCompleted {
		cmd.Printf("session %s was already merged and retired\n", session)
		return nil
	}
	cmd.Printf("%s: %d commits replayed and signed\n", session, len(result.Signed))
	for _, signed := range result.Signed {
		cmd.Printf("  %s\n", signed)
	}
	cmd.Printf("session %s removed from both sides\n", session)
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
	return store.Update(record.Name, func(latest *state.Record) error {
		latest.RemoveSession(session)
		return nil
	})
}

func runSessionDelete(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstanceForSession(cmd, name, args)
	if err != nil {
		return err
	}
	session := ""
	if len(args) > 0 {
		session = args[0]
	} else {
		sess, err := resolveSessionArg(cmd, record, args)
		if err != nil {
			return err
		}
		session = sess.Name
	}
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}
	result, err := lifecycle.RetireSession(progressCtx(cmd), store, record.Name, session, lifecycle.RetirementOptions{Operation: state.RetirementDelete, Force: force})
	if err != nil {
		return err
	}
	if result.AlreadyCompleted {
		cmd.Printf("session %s was already deleted\n", session)
		return nil
	}
	cmd.Printf("session %s deleted\n", session)
	return nil
}
