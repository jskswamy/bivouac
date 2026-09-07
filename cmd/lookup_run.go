package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// resolveInstance opens the state store, looks up name, and returns
// the same "no instance named" error every command below list/up
// reports identically when the name doesn't resolve to a known
// instance.
func resolveInstance(name string) (*state.Store, state.Record, error) {
	store, err := state.Open()
	if err != nil {
		return nil, state.Record{}, err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return nil, state.Record{}, err
	}
	if !ok {
		return nil, state.Record{}, fmt.Errorf("no instance named %q (run cloudlab up first)", name)
	}
	return store, record, nil
}

// resolveProvider builds a DigitalOcean provider from
// DIGITALOCEAN_TOKEN, for commands that need to call the live API
// (down, status).
func resolveProvider() (provider.Provider, error) {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DIGITALOCEAN_TOKEN not set")
	}
	return digitalocean.New(token), nil
}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}

	ok, err := confirm(cmd, downSummary(record))
	if err != nil {
		return err
	}
	if !ok {
		cmd.Println("Aborted.")
		return nil
	}

	if err := lifecycle.Down(cmd.Context(), p, store, record); err != nil {
		return err
	}
	cmd.Printf("Instance %s is down\n", name)
	return nil
}

// downSummary describes the instance down is about to destroy, and
// warns the destruction is unrecoverable, for confirmation before
// anything irreversible happens.
func downSummary(record state.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This will destroy instance %q -- this cannot be undone:\n", record.Name)
	fmt.Fprintf(&b, "  Provider: %s\n", record.Provider)
	fmt.Fprintf(&b, "  Region:   %s\n", record.Region)
	fmt.Fprintf(&b, "  Size:     %s\n", record.Size)
	fmt.Fprintf(&b, "  Template: %s\n", record.Template)
	fmt.Fprintf(&b, "  IP:       %s\n", record.IP)
	b.WriteString("Any unsaved work on the instance will be lost.\n")
	b.WriteString("Proceed? [y/N]: ")
	return b.String()
}

func runStatus(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}

	st := lifecycle.Status(cmd.Context(), p, record)
	cmd.Printf("Name:     %s\n", st.Record.Name)
	cmd.Printf("Provider: %s\n", st.Record.Provider)
	cmd.Printf("Region:   %s\n", st.Record.Region)
	cmd.Printf("Size:     %s\n", st.Record.Size)
	cmd.Printf("Template: %s\n", st.Record.Template)
	cmd.Printf("User:     %s\n", st.Record.User)
	repoPath := st.Record.RepoPath
	if repoPath == "" {
		repoPath = "(unknown -- provisioned before this field existed)"
	}
	cmd.Printf("RepoPath: %s\n", repoPath)
	cmd.Printf("IP:       %s\n", st.Record.IP)
	if st.LiveErr != nil {
		cmd.Printf("Status:   unknown (live check failed: %v)\n", st.LiveErr)
	} else {
		cmd.Printf("Status:   %s\n", st.LiveStatus)
	}

	watch, err := lifecycle.GetWatchStatus(cmd.Context(), name)
	if err != nil {
		cmd.Printf("Watch:    unknown (check failed: %v)\n", err)
	} else if !watch.Running {
		cmd.Printf("Watch:    not running\n")
	} else {
		cmd.Printf("Watch:    %s (alpha connected: %v, beta connected: %v, conflicts: %d)\n", watch.Status, watch.AlphaConnected, watch.BetaConnected, watch.Conflicts)
		if watch.LastError != "" {
			cmd.Printf("Error:    %s\n", watch.LastError)
		}
	}
	return nil
}

func runWatch(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoFlag, _ := cmd.Flags().GetString("repo")
	root, err := identity.RepoRoot(cwd, repoFlag)
	if err != nil {
		return err
	}

	// record.RepoPath is unset for instances provisioned before this
	// field existed (no migration, per the RemotePath rollout) --
	// recompute the same way Up would, and persist it so ssh's own
	// RepoPath read (runSSH, below) agrees with where watch is
	// actually syncing from now on instead of staying stuck at "".
	remotePath := record.RepoPath
	if remotePath == "" {
		remotePath, err = lifecycle.RemotePath(root, record.User)
		if err != nil {
			return err
		}
		record.RepoPath = remotePath
		if err := store.Put(record); err != nil {
			return err
		}
	}

	if err := lifecycle.StartWatch(cmd.Context(), record.IP, record.User, name, root, remotePath); err != nil {
		return err
	}
	cmd.Printf("Watch restarted for %s\n", name)
	return nil
}

// syncLocalDir returns the local directory sync should push: the
// --dir flag's value if set, else the current working directory.
func syncLocalDir(dirFlag string) (string, error) {
	if dirFlag != "" {
		return dirFlag, nil
	}
	return os.Getwd()
}

// syncRemoteDir returns the remote directory sync should target:
// args[0] if given, else local's path mirrored under remoteUser's
// home via lifecycle.RemotePath (always a mirror -- under the home
// path directly if local is under the local user's home, or under
// the full absolute local path otherwise).
func syncRemoteDir(args []string, local, remoteUser string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return lifecycle.RemotePath(local, remoteUser)
}

// runSessionStart backs `cloudlab session start <name>`. Cobra resolves the
// verb now, so there is no hand-rolled dispatch here and no unknown-subcommand
// error to maintain -- an unrecognised verb gets cobra's own suggestion.
func runSessionStart(cmd *cobra.Command, name string, args []string) error {
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
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoFlag, _ := cmd.Flags().GetString("repo")
	root, err := identity.RepoRoot(cwd, repoFlag)
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
	record.PutSession(state.Session{Name: session, LocalRepo: root, Base: base})
	if err := store.Put(record); err != nil {
		return err
	}
	if err := lifecycle.StartSession(ctx, record.IP, record.User, root, name, session); err != nil {
		return err
	}
	cmd.Printf("Session %s started on %s\n", session, name)
	cmd.Printf("\nLocal   %s\n", lifecycle.LocalWorktreePath(root, session))
	cmd.Printf("Fetch   cloudlab session pull %s\n", session)
	cmd.Printf("Accept  cloudlab session merge %s\n", session)
	return nil
}

func runSync(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	dirFlag, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	local, err := syncLocalDir(dirFlag)
	if err != nil {
		return err
	}
	remote, err := syncRemoteDir(args, local, record.User)
	if err != nil {
		return err
	}

	if err := lifecycle.Push(cmd.Context(), record.IP, record.User, local, remote); err != nil {
		return err
	}
	cmd.Printf("Synced %s to %s:%s\n", local, name, remote)
	return nil
}

// defaultLocalDir returns the local directory download uses when no
// local-dir is given: ./<basename(remote)>. remote is always a POSIX
// path (the instance is always Linux), so path.Base is used rather
// than filepath.Base.
func defaultLocalDir(remote string) string {
	return "./" + path.Base(path.Clean(remote))
}

func runDownload(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	remote := args[0]
	local := defaultLocalDir(remote)
	if len(args) > 1 {
		local = args[1]
	}

	if err := lifecycle.Pull(cmd.Context(), record.IP, record.User, remote, local); err != nil {
		return err
	}
	cmd.Printf("Downloaded %s:%s to %s\n", name, remote, local)
	return nil
}

// resolveSessionArg resolves the session a session-aware command acts on,
// along with the repository root it should operate in. Returns
// *lifecycle.AmbiguousError unchanged, so an interactive caller can offer a
// picker instead of refusing.
func resolveSessionArg(cmd *cobra.Command, record state.Record, args []string) (state.Session, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return state.Session{}, "", err
	}
	repoFlag, _ := cmd.Flags().GetString("repo")
	root, err := identity.RepoRoot(cwd, repoFlag)
	if err != nil {
		return state.Session{}, "", err
	}
	explicit := ""
	if len(args) > 0 {
		explicit = args[0]
	}
	sess, err := lifecycle.ResolveSession(cmd.Context(), cwd, record, explicit)
	if err != nil {
		return state.Session{}, root, err
	}
	return sess, root, nil
}

func runPull(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})
	sess, root, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	commits, err := lifecycle.PullSession(ctx, record.IP, record.User, root, name, sess.Name, sess.Base)
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
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})
	sess, root, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	signed, err := lifecycle.MergeSession(ctx, record.IP, record.User, root, name, sess.Name, sess.Base)
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

func runSSH(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	dir, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	if dir == "" {
		dir = record.RepoPath
	}
	return lifecycle.SSH(cmd.Context(), record.IP, record.User, dir)
}

func runHerdr(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	return lifecycle.Herdr(cmd.Context(), record.IP, record.User)
}

func runTailscale(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})
	if err := lifecycle.JoinTailscale(ctx, record.IP, record.User); err != nil {
		return err
	}
	record.TailscaleJoined = true
	if err := store.Put(record); err != nil {
		return err
	}
	cmd.Printf("%s joined the tailnet\n", name)
	return nil
}

func runPair(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	// The QR hands the phone an address to connect back to, so which one
	// it advertises is a real choice: the public IP works from anywhere,
	// the tailnet address keeps the session off the public internet but
	// only works from a device on the same tailnet. cloudlab itself
	// always connects over the public IP regardless (see lifecycle.Pair).
	advertise, err := cmd.Flags().GetString("host")
	if err != nil {
		return err
	}
	if advertise == "" {
		advertise, err = choosePairHost(cmd, record)
		if err != nil {
			return err
		}
	}
	return lifecycle.Pair(cmd.Context(), record.IP, advertise, record.User)
}

// choosePairHost returns the address the pairing QR should advertise.
// With no tailnet address available there is nothing to choose and the
// public IP is returned without prompting; otherwise the user picks,
// defaulting to the tailnet address as the more private of the two.
func choosePairHost(cmd *cobra.Command, record state.Record) (string, error) {
	if !record.TailscaleJoined {
		return record.IP, nil
	}
	tsIP, err := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
	if err != nil || tsIP == "" {
		// Not reachable over the tailnet is not a pairing failure --
		// fall back to the address that always works.
		return record.IP, nil
	}

	cmd.Printf("Which address should the QR advertise to the phone?\n")
	cmd.Printf("  1) %s  (Tailscale -- private, needs the phone on your tailnet)\n", tsIP)
	cmd.Printf("  2) %s  (public IP -- reachable anywhere)\n", record.IP)
	cmd.Print("Choose [1]: ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "2" {
		return record.IP, nil
	}
	return tsIP, nil
}

// defaultTmuxSession is the session cloudlab tmux creates-or-attaches
// to when no session-name argument is given.
const defaultTmuxSession = "main"

// tmuxSession returns the session name cloudlab tmux should
// create-or-attach: args[0] if given, else defaultTmuxSession.
func tmuxSession(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return defaultTmuxSession
}

func runTmux(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	return lifecycle.Tmux(cmd.Context(), record.IP, record.User, tmuxSession(args))
}
