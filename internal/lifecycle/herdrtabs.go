package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/shellcmd"
)

// herdrTab and herdrPane are entries from `herdr tab list` and `herdr pane
// list`. Like workspace ids, these are scoped to the instance's server and
// read per call, never remembered. A pane nobody has named has no label key
// at all, which decodes to "".
type herdrTab struct {
	ID    string `json:"tab_id"`
	Label string `json:"label"`
}

type herdrPane struct {
	ID    string `json:"pane_id"`
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

func parseTabList(out string) ([]herdrTab, error) {
	var reply struct {
		Result struct {
			Tabs []herdrTab `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return nil, fmt.Errorf("reading the herdr tab list: %w\n%s", err, out)
	}
	return reply.Result.Tabs, nil
}

func parsePaneList(out string) ([]herdrPane, error) {
	var reply struct {
		Result struct {
			Panes []herdrPane `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return nil, fmt.Errorf("reading the herdr pane list: %w\n%s", err, out)
	}
	return reply.Result.Panes, nil
}

// parseTabCreated reads the new tab's id and the root pane it opened with.
// The create reply is used here, unlike for workspaces, because the root
// pane has no label yet and so could not be found again in a listing.
func parseTabCreated(out string) (tabID, rootPane string, err error) {
	var reply struct {
		Result struct {
			Tab      herdrTab  `json:"tab"`
			RootPane herdrPane `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return "", "", fmt.Errorf("reading the herdr tab create reply: %w\n%s", err, out)
	}
	return reply.Result.Tab.ID, reply.Result.RootPane.ID, nil
}

// parsePaneSplit reads the id of the pane a split opened, for the same
// reason: it is unlabelled until renamed.
func parsePaneSplit(out string) (string, error) {
	var reply struct {
		Result struct {
			Pane herdrPane `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return "", fmt.Errorf("reading the herdr pane split reply: %w\n%s", err, out)
	}
	return reply.Result.Pane.ID, nil
}

// remoteHerdrCmd builds a herdr command to run on the instance over SSH.
//
// --session always, because bivouac addresses this session's server, not
// its default one. Wrapped in `bash -lc` for the same reason the tailscale
// commands are: herdr comes from the user's nix profile, and a bare
// non-login shell is not guaranteed to have it on PATH.
//
// Every EnsureTabs call runs over one already-open SSH connection rather
// than herdr's own --machine routing, which EnsureMachine/EnsureWorkspace/
// FocusWorkspace use. --machine measured at roughly 5s per invocation --
// a fixed connection-setup cost paid on every call, confirmed live, not
// amortized across calls -- against 24ms for a command run locally. That
// barely mattered for the 2-3 calls workspace/focus make, but EnsureTabs
// makes one call per tab and per pane beyond a tab's first (`pane split`
// has no --label, so a rename always follows), which was taking closer to
// a minute for a handful of tabs. One SSH connection plus fast remote
// execs is the same trade EnsureWorkspace made against a second SSH
// connection before it moved to --machine -- just the other way, because
// the call count here is high enough to flip which cost dominates.
func remoteHerdrCmd(session string, args ...string) string {
	inner := "herdr --session " + shellcmd.Quote(session)
	for _, a := range args {
		inner += " " + shellcmd.Quote(a)
	}
	return shellcmd.LoginShell(inner)
}

func tabListCmd(session, workspace string) string {
	return remoteHerdrCmd(session, "tab", "list", "--workspace", workspace)
}

// tabCreateCmd opens the tab in its first pane's directory, since that
// pane is the tab's own root pane rather than a split. --no-focus for the
// same reason as workspaces: laying out must not move anyone.
func tabCreateCmd(session, workspace, label, cwd string) string {
	return remoteHerdrCmd(session, "tab", "create", "--workspace", workspace,
		"--label", label, "--cwd", cwd, "--no-focus")
}

func tabRenameCmd(session, id, label string) string {
	return remoteHerdrCmd(session, "tab", "rename", id, label)
}

func tabCloseCmd(session, id string) string {
	return remoteHerdrCmd(session, "tab", "close", id)
}

func paneListCmd(session, workspace string) string {
	return remoteHerdrCmd(session, "pane", "list", "--workspace", workspace)
}

// paneSplitCmd always splits downward. herdr's own --help for `pane
// split` doesn't mark --direction as required, but the CLI rejects a
// split with no direction at all -- confirmed live, not from the docs
// -- so one has to be chosen; "down" reads as the natural continuation
// of a vertically-declared pane list, and there is no per-pane config
// for it, so every split uses the same one. Declaration order is still
// the whole topology the config expresses beyond that.
func paneSplitCmd(session, from, cwd string) string {
	return remoteHerdrCmd(session, "pane", "split", from, "--direction", "down", "--cwd", cwd, "--no-focus")
}

func paneRenameCmd(session, id, label string) string {
	return remoteHerdrCmd(session, "pane", "rename", id, label)
}

func paneRunCmd(session, id, command string) string {
	return remoteHerdrCmd(session, "pane", "run", id, command)
}

// paneDir is where a pane starts: its configured cwd, relative to the
// session's checkout, or the checkout itself.
//
// path, not filepath: this names a directory on the instance, which is
// Linux whatever this machine is.
func paneDir(root string, p config.HerdrPane) string {
	if p.Cwd == nil || *p.Cwd == "" {
		return root
	}
	// herdr accepts an absolute path or one rooted at "~" and resolves either
	// on the instance itself; joining it under root would nest it inside the
	// checkout instead of leaving it alone.
	if path.IsAbs(*p.Cwd) || *p.Cwd == "~" || strings.HasPrefix(*p.Cwd, "~/") {
		return *p.Cwd
	}
	return path.Join(root, *p.Cwd)
}

// EnsureTabs lays out the configured tabs and panes in the session's
// workspace, adding only what is not already there.
//
// Everything is matched by label, the same way the workspace is: ids are the
// instance server's, and the label is the only part bivouac chose. That
// also decides when a command runs -- only for a pane this call created. A
// second attach finds every label present and runs nothing, which is what
// keeps `npm run dev` from being typed into a pane already running it.
//
// herdr's own default tab ("1") is left alone here; LayOutTabs is what removes
// it, and only from a workspace this attach created.
//
// Runs over r, one already-open SSH connection, rather than herdr's own
// --machine routing -- see remoteHerdrCmd for why: --machine's per-call
// cost is fixed and paid on every invocation, and this makes one call per
// tab plus one more per pane beyond a tab's first.
func EnsureTabs(r remoteRunner, session, workspaceID, root string, tabs []config.HerdrTab) error {
	return ensureTabs(r, session, workspaceID, root, tabs, nil)
}

func ensureTabs(r remoteRunner, session, workspaceID, root string, tabs []config.HerdrTab, spare *spareTab) error {
	if len(tabs) == 0 {
		return nil
	}
	out, err := r.Run(tabListCmd(session, workspaceID))
	if err != nil {
		return fmt.Errorf("listing tabs: %w\n%s", err, out)
	}
	existing, err := parseTabList(out)
	if err != nil {
		return err
	}
	out, err = r.Run(paneListCmd(session, workspaceID))
	if err != nil {
		return fmt.Errorf("listing panes: %w\n%s", err, out)
	}
	panes, err := parsePaneList(out)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var errs []error
	for _, t := range tabs {
		// Skip duplicate tab labels. Label is the identity, so two same-label
		// entries could never both be matched on re-attach anyway.
		if seen[t.Label] {
			continue
		}
		seen[t.Label] = true
		// Collect and keep going rather than stopping at the first failure:
		// one tab that persistently fails (a cwd that doesn't exist on the
		// instance, say) must not silently skip every tab after it on every
		// attach. errors.Join reports every failure this pass found; nil
		// when there were none.
		if err := ensureTab(r, session, workspaceID, root, t, existing, panes, spare); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LayOutTabs is EnsureTabs plus dealing with herdr's own default tab.
//
// herdr gives every new workspace an empty tab "1" with a root pane, and
// creating the configured tabs beside it left it as clutter in front. In a
// workspace this attach just created, the first configured tab takes it over
// instead: renamed, with its root pane as the tab's first pane, so nothing
// is created only to be closed. A root pane starts in the checkout and cannot
// be moved, so when the first pane needs its own directory the tabs are
// created as usual and the default tab closed once they exist.
//
// Only when fresh: a re-attach cannot tell an untouched tab 1 from one the
// user works in, and renaming or closing that would take their shell away.
// It is also left alone when it was not the workspace's only tab, when it
// holds more than one pane, and when a configured tab has its label, since
// EnsureTabs would reuse it as it stands. When nothing is configured tab 1 is
// the only tab and there is nothing to do.
//
// Closing is tidying, not laying out: a failure to close is returned so the
// user hears about it, with the layout already done.
func LayOutTabs(r remoteRunner, session, workspaceID, root string, tabs []config.HerdrTab, fresh bool) error {
	if len(tabs) == 0 {
		return nil
	}
	var spare *spareTab
	if fresh {
		spare = pristineDefaultTab(r, session, workspaceID, tabs)
	}
	if err := ensureTabs(r, session, workspaceID, root, tabs, spare); err != nil {
		return err
	}
	if spare == nil || spare.used {
		return nil
	}
	if out, err := r.Run(tabCloseCmd(session, spare.tabID)); err != nil {
		return fmt.Errorf("closing herdr's default tab: %w\n%s", err, out)
	}
	return nil
}

// spareTab is herdr's own default tab and its root pane, offered to the
// first configured tab that has to be created. used says it was taken.
type spareTab struct {
	tabID    string
	rootPane string
	used     bool
}

// pristineDefaultTab returns the workspace's default tab when it is provably
// the one herdr just made: the workspace's only tab, holding a single pane.
// Anything less certain returns nil, and then nothing is touched.
func pristineDefaultTab(r remoteRunner, session, workspaceID string, tabs []config.HerdrTab) *spareTab {
	out, err := r.Run(tabListCmd(session, workspaceID))
	if err != nil {
		return nil
	}
	existing, err := parseTabList(out)
	if err != nil || len(existing) != 1 || usesLabel(tabs, existing[0].Label) {
		return nil
	}
	out, err = r.Run(paneListCmd(session, workspaceID))
	if err != nil {
		return nil
	}
	panes, err := parsePaneList(out)
	if err != nil {
		return nil
	}
	inTab := panesInTab(panes, existing[0].ID)
	if len(inTab) != 1 {
		return nil
	}
	return &spareTab{tabID: existing[0].ID, rootPane: inTab[0].ID}
}

func usesLabel(tabs []config.HerdrTab, label string) bool {
	for _, t := range tabs {
		if t.Label == label {
			return true
		}
	}
	return false
}

// ensureTab brings one tab up to its configuration.
//
// Panes chain in declaration order: the first is the tab's root pane and
// each later one splits off the pane before it. When a pane is already
// there it becomes the next split's anchor, so restoring one closed pane
// puts it back beside its neighbour rather than at the end.
func ensureTab(r remoteRunner, session, workspaceID, root string, t config.HerdrTab, existing []herdrTab, panes []herdrPane, spare *spareTab) error {
	var tabID, fresh string
	if found, ok := findTab(existing, t.Label); ok {
		tabID = found.ID
	} else {
		dir := root
		if len(t.Panes) > 0 {
			dir = paneDir(root, t.Panes[0])
		}
		if spare != nil && !spare.used && dir == root {
			// Takes herdr's default tab over rather than adding one beside it.
			if out, err := r.Run(tabRenameCmd(session, spare.tabID, t.Label)); err != nil {
				return fmt.Errorf("naming herdr's default tab %s: %w\n%s", t.Label, err, out)
			}
			spare.used = true
			tabID, fresh = spare.tabID, spare.rootPane
		} else {
			out, err := r.Run(tabCreateCmd(session, workspaceID, t.Label, dir))
			if err != nil {
				return fmt.Errorf("creating the %s tab: %w\n%s", t.Label, err, out)
			}
			if tabID, fresh, err = parseTabCreated(out); err != nil {
				return err
			}
		}
	}

	inTab := panesInTab(panes, tabID)
	prev := fresh
	// When an existing tab is missing its first declared pane (closed since
	// the last attach, say), it is restored as a split off the first
	// surviving pane rather than as the tab's root -- only `tab create`
	// produces a true root pane, and this tab already has one. Its label and
	// command still land correctly; only its position in the tab differs.
	if len(inTab) > 0 {
		prev = inTab[0].ID
	}
	seenPane := make(map[string]bool)
	for i, p := range t.Panes {
		// Skip duplicate pane labels. Label is the identity, so two same-label
		// entries could never both be matched on re-attach anyway.
		if seenPane[p.Label] {
			continue
		}
		seenPane[p.Label] = true
		if found, ok := findPane(inTab, p.Label); ok {
			prev = found.ID
			continue
		}
		id := fresh
		if i > 0 || fresh == "" {
			out, err := r.Run(paneSplitCmd(session, prev, paneDir(root, p)))
			if err != nil {
				return fmt.Errorf("opening the %s pane in the %s tab: %w\n%s", p.Label, t.Label, err, out)
			}
			if id, err = parsePaneSplit(out); err != nil {
				return err
			}
		}
		if out, err := r.Run(paneRenameCmd(session, id, p.Label)); err != nil {
			return fmt.Errorf("labelling the %s pane in the %s tab: %w\n%s", p.Label, t.Label, err, out)
		}
		// Rename before run, not after. If run fails, the pane is already
		// labelled, and a re-attach will never retry the command. If rename
		// fails, we report it and the caller surfaces it as a warning naming
		// the pane; run-then-rename would duplicate the command whenever
		// rename fails. The hard rule is never to run twice.
		if p.Command != nil && *p.Command != "" {
			if out, err := r.Run(paneRunCmd(session, id, *p.Command)); err != nil {
				return fmt.Errorf("starting %q in the %s pane: %w\n%s", *p.Command, p.Label, err, out)
			}
		}
		prev = id
	}
	return nil
}

func findTab(tabs []herdrTab, label string) (herdrTab, bool) {
	for _, t := range tabs {
		if t.Label == label {
			return t, true
		}
	}
	return herdrTab{}, false
}

func panesInTab(panes []herdrPane, tabID string) []herdrPane {
	var out []herdrPane
	for _, p := range panes {
		if p.TabID == tabID {
			out = append(out, p)
		}
	}
	return out
}

func findPane(panes []herdrPane, label string) (herdrPane, bool) {
	for _, p := range panes {
		if p.Label == label {
			return p, true
		}
	}
	return herdrPane{}, false
}
