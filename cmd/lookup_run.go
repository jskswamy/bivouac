package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jskswamy/cloudlab/internal/config"
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

// resolveProvider builds a DigitalOcean provider from the token
// resolveToken finds, for the three commands that call the live API
// (up, down, status). Every other command works off state.Record and
// SSH and needs no token at all.
func resolveProvider(ctx context.Context) (provider.Provider, error) {
	token, err := resolveToken(ctx)
	if err != nil {
		return nil, err
	}
	return digitalocean.New(token), nil
}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})
	p, err := resolveProvider(ctx)
	if err != nil {
		return err
	}

	force, err := cmd.Flags().GetBool("force")
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

	if err := lifecycle.Down(cmd.Context(), p, store, record, force); err != nil {
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

// servingLookupTimeout bounds what status is willing to wait for the
// instance's published ports.
//
// Generous enough for a loaded instance -- the box this was found on was
// swapping under three concurrent sessions and still answered other
// commands in single-digit seconds -- and short enough that a report stays
// a report. Anything slower is indistinguishable from unreachable, and
// status says so rather than waiting to find out.
const servingLookupTimeout = 20 * time.Second

func runStatus(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})

	// status's only use of the API is the live status field; every other
	// line it prints comes from local state. lifecycle.Status already
	// reports a failed live check rather than failing the call -- an
	// absent token is the same kind of fact, so it degrades the same way
	// rather than making the whole report unavailable on a machine with
	// no key present. up and down still fail hard: neither can create or
	// destroy a droplet without the API, so for them a missing token is
	// not a degraded report but no work at all.
	st := lifecycle.InstanceStatus{Record: record}
	liveReason := "live check failed"
	if p, provErr := resolveProvider(ctx); provErr != nil {
		st.LiveErr = provErr
		liveReason = "no live check"
	} else {
		st = lifecycle.Status(ctx, p, record)
	}
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
		cmd.Printf("Status:   unknown (%s: %v)\n", liveReason, st.LiveErr)
	} else {
		cmd.Printf("Status:   %s\n", st.LiveStatus)
	}
	printSessions(cmd, record)

	// Only when state says there is a tailnet: serving is tailnet-only,
	// so an instance that never joined has nothing to report and should
	// not pay an SSH round trip to learn that.
	if record.TailscaleJoined {
		// Bounded because status is a report. An instance stops answering
		// for reasons this side cannot tell apart -- it ran out of memory,
		// a daemon wedged, the tailnet went down, the connection went
		// half-open -- and status should survive all of them rather than
		// try to diagnose any. Without this the report printed everything
		// up to Serving and then hung, which is worse than the "unknown"
		// it was already written to fall back to.
		//
		// Derived from ctx rather than cmd.Context(): ctx carries the
		// progress reporter built above, and starting again from the bare
		// command context would silently drop it.
		serveCtx, cancel := context.WithTimeout(ctx, servingLookupTimeout)
		defer cancel()

		// Said before the wait, not after. This is the one step in status
		// that can take twenty seconds, and an unexplained pause after the
		// last printed line is what a hang looks like -- the report should
		// not have to be timed to tell the difference.
		provider.ReportProgress(ctx, "checking published ports")

		entries, serveErr := lifecycle.ServeStatus(serveCtx, record.IP, record.User)
		// The tailnet IP is only needed to print an address beside each
		// entry, so it is fetched only when there is an entry to print --
		// skipping a second SSH round trip both when the instance is
		// unreachable (serveErr already answers that) and in the common
		// case of nothing being served.
		var tailnetIP string
		if serveErr == nil && len(entries) > 0 {
			tailnetIP, _ = lifecycle.TailscaleIP(serveCtx, record.IP, record.User)
		}
		printServing(cmd, entries, tailnetIP, serveErr)
	}
	return nil
}

// printServing renders the instance's published ports. Separate from
// runStatus for the same reason printSessions is: it can then be tested
// without a provider or an instance.
//
// An error is reported as unknown rather than returned. status is a
// read-only report and an unreachable instance is an expected state for
// it -- the live provider check above already renders that way, and a
// serve lookup must not be the thing that makes status fail.
func printServing(cmd *cobra.Command, entries []lifecycle.ServeEntry, tailnetIP string, err error) {
	if err != nil {
		cmd.Printf("Serving:  unknown (instance unreachable)\n")
		return
	}
	if len(entries) == 0 {
		cmd.Printf("Serving:  none\n")
		return
	}
	cmd.Printf("Serving:\n")
	for _, e := range entries {
		// The ports are known even when the tailnet lookup that would
		// address them is not -- worth showing rather than discarding,
		// but never as a bare ":8888" with no host.
		addr := "(tailnet address unknown)"
		if tailnetIP != "" {
			addr = tailnetIP + ":" + strconv.Itoa(e.Port)
		}
		cmd.Printf("  %-6d %s\n", e.Port, addr)
	}
}

// printSessions renders an instance's sessions. Separate from runStatus so it
// can be tested without a provider: runStatus reaches lifecycle.Status, which
// makes a live API call, and the session section has nothing to do with that.
func printSessions(cmd *cobra.Command, record state.Record) {
	if len(record.Sessions) == 0 {
		cmd.Printf("Sessions: none\n")
		return
	}
	cmd.Printf("Sessions:\n")
	for _, info := range lifecycle.DescribeSessions(cmd.Context(), record.Name, record.Sessions) {
		wtState := "clean"
		if !info.WorktreeExists {
			wtState = "no worktree"
		} else if info.WorktreeDirty {
			wtState = "dirty"
		}
		cmd.Printf("  %-16s %-16s %s, %s\n", info.Name, info.Branch, unmergedLabel(info), wtState)
	}
}

// unmergedLabel renders what a session's work amounts to, in the one column
// a user scans to decide whether to pull.
//
// Three things can be true and the label has to keep them apart, because
// collapsing any two of them is the bug this replaced: an exact count, a
// known-ahead session whose commits have not been fetched so cannot be
// counted, and an instance that never answered.
//
// The unreachable case keeps its number when it has one -- the local branch
// is still the freshest thing this machine holds -- but says where it came
// from. A bare "0" from an instance nobody could reach is precisely how a
// waiting commit went unnoticed.
func unmergedLabel(i lifecycle.SessionInfo) string {
	count := "?"
	if i.UnmergedKnown {
		count = fmt.Sprintf("%d", i.Unmerged)
	}
	if !i.RemoteKnown {
		return count + " unmerged (instance unreachable)"
	}
	if !i.UnmergedKnown {
		if i.RemoteAhead {
			return "ahead (pull to count)"
		}
		return "? unmerged"
	}
	return count + " unmerged"
}

// beadsModeFor reads the beads setting out of the repository's cloudlab.pkl.
//
// Best-effort by design. `cloudlab session start` does not require pkl today,
// and beads must not be the reason it starts to: a config that will not
// resolve is a problem `up` and `provision` report properly, with a better
// message than this could give. The fallback is inert on a repository with no
// .beads/, which is the overwhelmingly common case.
func beadsModeFor(ctx context.Context, root string) string {
	cfg, err := config.Resolve(ctx, filepath.Join(root, "cloudlab.pkl"))
	if err != nil {
		provider.ReportWarning(ctx, "beads: could not resolve "+root+"'s config ("+err.Error()+"); falling back to session mode")
		return "session"
	}
	return cfg.Beads
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
	if err := lifecycle.StartSession(ctx, record.IP, record.User, root, name, session, beadsModeFor(ctx, root)); err != nil {
		return err
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
	for _, i := range infos {
		wtState := "clean"
		if !i.WorktreeExists {
			wtState = "no worktree"
		} else if i.WorktreeDirty {
			wtState = "dirty"
		}
		cmd.Printf("%-16s %-24s %-16s %s, %s\n", i.Name, i.Instance, i.Branch, unmergedLabel(i), wtState)
	}
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
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	ctx := provider.WithProgress(cmd.Context(), func(status string) {
		cmd.Printf("→ %s\n", status)
	})
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	commits, err := lifecycle.PullSession(ctx, record.IP, record.User, sess.LocalRepo, name, sess.Name, sess.Base)
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
	sess, err := resolveSessionArg(cmd, record, args)
	if err != nil {
		return err
	}
	signed, err := lifecycle.MergeSession(ctx, record.IP, record.User, sess.LocalRepo, name, sess.Name, sess.Base)
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
	store, record, err := resolveInstance(name)
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
	warning, err := lifecycle.DeleteSession(cmd.Context(), record.IP, record.User, name, sess, force)
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

// errDiscoveryNoPort, errNoListeners and errAskUser are sentinels
// chooseListener returns when finishing the decision needs something it
// doesn't have: the discovery error itself and the instance name (both
// kept by the caller, which wraps them into the final message), or a
// terminal to prompt on. Named errors here are less code than widening
// the signature to a third return value every unambiguous caller would
// have to ignore.
var (
	errDiscoveryNoPort = errors.New("discovery failed, no port given")
	errNoListeners     = errors.New("no listeners")
	errAskUser         = errors.New("ask the user")
	errNoServed        = errors.New("nothing is being served")
)

// portVocabulary is a caller's own words for "give me a port" and "go
// find out what's available", so chooseListener's messages can speak
// connect's flag or serve's positional without knowing which one called
// it. connect and serve name a port two different ways -- a --port flag
// versus a bare positional -- and a message that assumes the former
// sends a serve user chasing a flag cobra will reject.
type portVocabulary struct {
	pick     string // e.g. "pass --port" or "name a port"
	recovery string // e.g. "run `cloudlab connect` with no --port to see what is"
}

var connectPortVocabulary = portVocabulary{
	pick:     "pass --port",
	recovery: "run `cloudlab connect` with no --port to see what is",
}

var servePortVocabulary = portVocabulary{
	pick:     "name a port",
	recovery: "run `cloudlab serve` with no port to see what is",
}

// chooseListener decides which listener runConnect (or runServe) targets,
// given what discovery found (or didn't). It is the pure half of that
// decision -- pulled out so the ladder itself is table-testable without a
// fake SSH server, the same reason sshArgs/tmuxArgs/herdrArgs/pairArgs
// exist as pure functions in internal/lifecycle. The interactive prompt is
// not pure (it reads a terminal), so ambiguity with more than one candidate
// comes back as errAskUser for the caller to act on, rather than being
// resolved here.
func chooseListener(listeners []lifecycle.Listener, port int, discoveryFailed, interactive bool, vocab portVocabulary) (lifecycle.Listener, error) {
	switch {
	case discoveryFailed && port == 0:
		return lifecycle.Listener{}, errDiscoveryNoPort
	case discoveryFailed:
		// No listing to check against, so the bind address is unknown.
		// Forward rather than guess: a forward reaches a service on any
		// address, while a tailnet URL reaches only a routable one.
		return lifecycle.Listener{Addr: "127.0.0.1", Port: port}, nil
	case port != 0:
		for _, l := range listeners {
			if l.Port == port {
				return l, nil
			}
		}
		return lifecycle.Listener{}, fmt.Errorf("nothing is listening on port %d — %s", port, vocab.recovery)
	case len(listeners) == 0:
		return lifecycle.Listener{}, errNoListeners
	case len(listeners) == 1:
		// One candidate is not a choice -- neither a prompt nor a
		// refusal is warranted when there is nothing to pick between.
		return listeners[0], nil
	case !interactive:
		return lifecycle.Listener{}, fmt.Errorf("several ports are listening; %s (no terminal to ask on)", vocab.pick)
	default:
		return lifecycle.Listener{}, errAskUser
	}
}

// chooseServeEntry is chooseListener's sibling for ports that are already
// published, but not the same shape: it has no discovery-failure branches
// (unserve always lists what tailscale has on file, nothing to fall back
// blind on), and the empty check runs FIRST rather than after the
// port-match check. That order is deliberate -- `unserve 8888` against an
// instance serving nothing should get the friendly "nothing is being
// served" message from runUnserve's errNoServed branch, not a bare "port
// 8888 is not being served" that implies something else is.
func chooseServeEntry(entries []lifecycle.ServeEntry, port int, interactive bool) (lifecycle.ServeEntry, error) {
	switch {
	case len(entries) == 0:
		return lifecycle.ServeEntry{}, errNoServed
	case port != 0:
		for _, e := range entries {
			if e.Port == port {
				return e, nil
			}
		}
		return lifecycle.ServeEntry{}, fmt.Errorf("port %d is not being served — run `cloudlab unserve` with no port to see what is", port)
	case len(entries) == 1:
		return entries[0], nil
	case !interactive:
		return lifecycle.ServeEntry{}, fmt.Errorf("several ports are being served; name one (no terminal to ask on)")
	default:
		return lifecycle.ServeEntry{}, errAskUser
	}
}

// discoverAndChoose runs the shared listener discovery both connect and
// serve need: list what is listening, hide the machine's own sockets
// unless asked, and settle on one candidate. It also hands back the raw
// offered/listeners slices, since both callers report afterward when a
// sole candidate was auto-selected and how many sockets a filter hid.
//
// tolerateDiscoveryFailure is what separates the two callers. connect can
// still forward to a port the user named without a listing, since a
// forward reaches any bind address; serve cannot, because the bind address
// is exactly what decides whether an entry is needed at all.
//
// vocab supplies chooseListener's caller-specific wording -- connect names
// a port with --port, serve with a bare positional -- so the same ladder
// can tell either caller's user how to retry without borrowing the other
// command's syntax.
func discoverAndChoose(cmd *cobra.Command, record state.Record, name string, port int, all, tolerateDiscoveryFailure bool, vocab portVocabulary) (chosen lifecycle.Listener, offered, listeners []lifecycle.Listener, err error) {
	// Discovery is best-effort when tolerated. `ss` comes from iproute2 and
	// is present on every image cloudlab boots, but an unusual base image
	// or a locked-down PATH should not make connect unusable when the
	// caller already knows the port. serve cannot make the same trade --
	// the bind address is exactly what decides whether an entry is needed
	// at all -- so a failed listing is fatal there instead.
	listeners, lerr := lifecycle.Listeners(cmd.Context(), record.IP, record.User)
	if lerr != nil && !tolerateDiscoveryFailure {
		return lifecycle.Listener{}, nil, nil, lerr
	}

	// A named port addresses a socket directly, so it searches everything:
	// a user who names one has said what they want, and hiding it would
	// only produce a puzzling "nothing is listening" for a port they can
	// see is open. The filter narrows what gets *offered*, not what can
	// be reached.
	offered = lifecycle.VisibleListeners(listeners, all || port != 0)

	chosen, err = chooseListener(offered, port, tolerateDiscoveryFailure && lerr != nil, isInteractive(), vocab)
	switch {
	case errors.Is(err, errDiscoveryNoPort):
		return lifecycle.Listener{}, offered, listeners, fmt.Errorf("%w\npass --port to connect without discovery", lerr)
	case errors.Is(err, errNoListeners):
		// Say so when the filter is why nothing is on offer, rather than
		// claiming an instance running seven sockets is running none.
		if hidden := len(listeners) - len(offered); hidden > 0 {
			return lifecycle.Listener{}, offered, listeners, fmt.Errorf("nothing of yours is listening on %s (%d infrastructure socket(s) hidden — pass --all to see them)", name, hidden)
		}
		return lifecycle.Listener{}, offered, listeners, fmt.Errorf("nothing is listening on %s", name)
	case errors.Is(err, errAskUser):
		if chosen, err = pickListener(cmd, offered); err != nil {
			return lifecycle.Listener{}, offered, listeners, err
		}
	case err != nil:
		return lifecycle.Listener{}, offered, listeners, err
	}
	return chosen, offered, listeners, nil
}

// runConnect reaches a service on the instance. With a port it goes
// straight there; without one it asks the instance what is listening.
func runConnect(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	// args[0] is the instance name (named: true), so a port comes from
	// the flag rather than a positional -- same reason runSSH takes
	// --dir rather than a second positional.
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return err
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}

	chosen, offered, listeners, err := discoverAndChoose(cmd, record, name, port, all, true, connectPortVocabulary)
	if err != nil {
		return err
	}

	// Say what was picked when the user did not pick it. A sole
	// candidate is auto-selected rather than prompted for, and the
	// filter is usually why there is only one -- without this line the
	// menu simply vanishes and the command starts reaching something
	// the user never named.
	if port == 0 && len(offered) == 1 {
		what := chosen.Process
		if what == "" {
			what = "unknown process"
		}
		cmd.Printf("Only one service is listening: %d (%s)\n", chosen.Port, what)
		if hidden := len(listeners) - len(offered); hidden > 0 {
			cmd.Printf("  %d infrastructure socket(s) hidden — pass --all to see them\n", hidden)
		}
	}

	// Skip the round trip entirely when state already says there is no
	// tailnet -- an extra SSH connect plus `tailscale ip` to relearn
	// what record.TailscaleJoined already told us. Mirrors the same
	// guard on choosePairHost below.
	var tailnetIP string
	if record.TailscaleJoined {
		tailnetIP, err = lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
		if err != nil {
			// Not reachable over the tailnet is an expected state, not a
			// failure -- ConnectTarget treats an empty tailnetIP as "forward
			// instead of routing there directly".
			tailnetIP = ""
		}
	}

	// Routable: nothing to set up, so nothing can fail after this line.
	// chosen.Port stands in for the local port here only to satisfy the
	// signature -- this branch never forwards, so it goes unused.
	routableURL, mustForward := lifecycle.ConnectTarget(tailnetIP, chosen, chosen.Port)
	if !mustForward {
		cmd.Println(routableURL)
		return nil
	}

	// The near side is settled BEFORE anything is printed. Otherwise the
	// "forwarding over SSH" line goes out first and a failed bind
	// contradicts it two lines later, which is the success-shaped-output
	// problem this command has already been bitten by once.
	wanted, err := cmd.Flags().GetInt("local-port")
	if err != nil {
		return err
	}
	mustUse := wanted != 0
	if !mustUse {
		wanted = chosen.Port
	}
	localPort, err := lifecycle.FreeLocalPort(wanted, mustUse)
	if err != nil {
		if errors.Is(err, lifecycle.ErrLocalPortBusy) {
			return fmt.Errorf("%w — something already holds it locally; pass a different --local-port, or omit the flag to let cloudlab pick a free one", err)
		}
		return err
	}
	// Only when cloudlab picked the number. Saying "is taken, forwarding
	// through N instead" to someone who asked for N reads as though the
	// request was overridden.
	if !mustUse && localPort != chosen.Port {
		cmd.Printf("Local port %d is taken, forwarding through %d instead\n", chosen.Port, localPort)
	}

	url, _ := lifecycle.ConnectTarget(tailnetIP, chosen, localPort)
	cmd.Printf("%s (forwarding over SSH — Ctrl-C to stop)\n", url)
	// Over the tailnet when there is one. Reaching this line means
	// TailscaleIP already answered, so falling back to the public IP
	// here would route around a link just proven to be up.
	host := record.IP
	if tailnetIP != "" {
		host = tailnetIP
	}
	return lifecycle.Forward(cmd.Context(), host, record.User, localPort, chosen.Port)
}

// serveTarget returns the tailnet URL a listener will answer at, and
// whether publishing it requires a serve entry at all.
//
// Split out as a pure function for the same reason ConnectTarget was:
// the routable-versus-loopback rule is the part worth testing, and it
// needs no network to test.
func serveTarget(tailnetIP string, l lifecycle.Listener) (string, bool) {
	scheme := ""
	if l.HTTP() {
		scheme = "http://"
	}
	return scheme + tailnetIP + ":" + strconv.Itoa(l.Port), l.LoopbackOnly()
}

// runServe publishes a service on the tailnet, where it outlives this
// command. Discovery is identical to connect's: the two commands differ
// in how long the result lasts, not in how you name what you want.
func runServe(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// Checked before any SSH work: serving is meaningless without a
	// tailnet, and this says so instead of surfacing a tailscale error.
	if !record.TailscaleJoined {
		return fmt.Errorf("%s is not on a tailnet — serving publishes there, so run `cloudlab tailscale` first", name)
	}

	port := 0
	if len(args) > 0 {
		if port, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("%q is not a port number", args[0])
		}
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}

	chosen, offered, listeners, err := discoverAndChoose(cmd, record, name, port, all, false, servePortVocabulary)
	if err != nil {
		return err
	}

	// Say what was picked when the user did not pick it -- the same notice
	// runConnect prints, and more warranted here: a forward dies with the
	// command, but this publishes on the tailnet until `unserve` undoes it,
	// so silently picking the wrong service is a mistake that outlives the
	// command that made it.
	if port == 0 && len(offered) == 1 {
		what := chosen.Process
		if what == "" {
			what = "unknown process"
		}
		cmd.Printf("Only one service is listening: %d (%s)\n", chosen.Port, what)
		if hidden := len(listeners) - len(offered); hidden > 0 {
			cmd.Printf("  %d infrastructure socket(s) hidden — pass --all to see them\n", hidden)
		}
	}

	tailnetIP, err := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
	if err != nil || tailnetIP == "" {
		return fmt.Errorf("could not resolve %s's tailnet address — is tailscaled running there?", name)
	}

	url, needsEntry := serveTarget(tailnetIP, chosen)
	if !needsEntry {
		cmd.Printf("%s is already reachable on the tailnet, no serving needed\n", url)
		return nil
	}
	if err := lifecycle.Serve(cmd.Context(), record.IP, record.User, chosen.Port); err != nil {
		return err
	}
	cmd.Printf("Serving %s:%d on your tailnet\n", chosen.Addr, chosen.Port)
	cmd.Printf("  %s\n", url)
	cmd.Printf("Stop with: cloudlab unserve %d\n", chosen.Port)
	return nil
}

// runUnserve stops publishing a port. It can only stop SERVED entries —
// a running `connect` forward is a foreground process in another
// terminal with no PID recorded, so the empty-state message says so
// rather than leaving someone to wonder why unserve did nothing.
func runUnserve(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// Same guard as runServe/runStatus: serving is tailnet-only, so an
	// instance that never joined has nothing to unserve and should not
	// pay a full SSH connect plus a `command -v tailscale` probe just to
	// be told that.
	if !record.TailscaleJoined {
		return fmt.Errorf("%s is not on a tailnet — nothing is being served there", name)
	}

	port := 0
	if len(args) > 0 {
		if port, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("%q is not a port number", args[0])
		}
	}

	entries, err := lifecycle.ServeStatus(cmd.Context(), record.IP, record.User)
	if err != nil {
		return err
	}

	chosen, err := chooseServeEntry(entries, port, isInteractive())
	switch {
	case errors.Is(err, errNoServed):
		return fmt.Errorf("nothing is being served on %s — a running `cloudlab connect` forward stops with Ctrl-C in its own terminal", name)
	case errors.Is(err, errAskUser):
		if chosen, err = pickServeEntry(cmd, entries); err != nil {
			return err
		}
	case err != nil:
		return err
	}

	if err := lifecycle.Unserve(cmd.Context(), record.IP, record.User, chosen.Port); err != nil {
		return err
	}
	cmd.Printf("Stopped serving %d\n", chosen.Port)
	return nil
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
		// record.RepoPath is a mirror of the local checkout path that rsync
		// used to create; nothing creates it now, so it silently landed the
		// user in $HOME. The session's repository is where the work is.
		//
		// nil, not args: for ssh and herdr args[0] is the INSTANCE name
		// (named: true), and for tmux it is a tmux session name -- a
		// different namespace. Passing either through would resolve it as a
		// cloudlab session and fail with "instance X has no session X".
		// Resolution here comes from the cwd, the single session, or the
		// picker; naming one explicitly is what `cd` into its worktree is for.
		if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
			dir = lifecycle.RemoteRepoPath(record.User, sess.Name, name)
		}
	}
	return lifecycle.SSH(cmd.Context(), record.IP, record.User, dir)
}

func runHerdr(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// nil, not args: args[0] here is the INSTANCE name (see the nil-args
	// comment on runSSH). herdr has no way to attach a starting directory to
	// a --remote session (that's cloudlab-7y1, via `workspace create --cwd`
	// at `session start` time) -- but it does accept --session, so the
	// resolved cloudlab session still buys a per-session herdr session:
	// reconnecting to the same session name lands back in the same place.
	// With no session resolvable, connect anyway with herdr's own default
	// session -- connecting is not destructive and must degrade, not refuse.
	session := ""
	if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
		session = sess.Name
	}
	return lifecycle.Herdr(cmd.Context(), record.IP, record.User, session)
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
	session := tmuxSession(args)
	// nil, not args: args[0] here is a tmux session name, a different
	// namespace from a cloudlab session name (see the nil-args comment on
	// runSSH). Only substitute the cloudlab session's name when the caller
	// didn't already ask for a specific tmux session -- reconnecting to that
	// same name is what lands back in the same place.
	if len(args) == 0 {
		if sess, err := resolveSessionInteractive(cmd, record, nil); err == nil {
			session = sess.Name
		}
	}
	return lifecycle.Tmux(cmd.Context(), record.IP, record.User, session)
}
