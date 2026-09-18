package lifecycle

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
)

func strp(s string) *string { return &s }

// runTab is the layout most tests use: a server pane, and a logs pane split
// off it in a subdirectory.
var runTab = config.HerdrTab{Label: "run", Panes: []config.HerdrPane{
	{Label: "server", Command: strp("npm run dev")},
	{Label: "logs", Command: strp("tail -f server.log"), Cwd: strp("logs")},
}}

const (
	onlyDefaultTab = `{"result":{"tabs":[{"label":"1","tab_id":"w3:t1","workspace_id":"w3"}],"type":"tab_list"}}`
	onlyRootPane   = `{"result":{"panes":[{"pane_id":"w3:p1","tab_id":"w3:t1","workspace_id":"w3"}],"type":"pane_list"}}`
	tabCreated     = `{"result":{"root_pane":{"pane_id":"w3:p2","tab_id":"w3:t2"},"tab":{"label":"run","tab_id":"w3:t2"},"type":"tab_created"}}`
	paneSplit      = `{"result":{"pane":{"pane_id":"w3:p3","tab_id":"w3:t2"},"type":"pane_info"}}`
)

const checkout = "/home/u/sessions/auth/repo"

// splitQuoted tokenizes a command string built from single-quoted words
// (shellcmd.Quote's own escaping: an embedded quote becomes '\”), which is
// the only quoting remoteHerdrCmd and shellcmd.LoginShell ever produce.
func splitQuoted(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		var b strings.Builder
		if s[i] == '\'' {
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if strings.HasPrefix(s[i:], `'\''`) {
						b.WriteByte('\'')
						i += 4
						continue
					}
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
		} else {
			for i < len(s) && s[i] != ' ' {
				b.WriteByte(s[i])
				i++
			}
		}
		out = append(out, b.String())
	}
	return out
}

// parseRemoteHerdrCmd unpacks a command remoteHerdrCmd built: the login
// shell wrapper, then "herdr --session <session> <args...>".
func parseRemoteHerdrCmd(cmd string) (session string, args []string, err error) {
	top := splitQuoted(cmd)
	if len(top) != 3 || top[0] != "bash" || top[1] != "-lc" {
		return "", nil, fmt.Errorf("not a login-shell command: %v", top)
	}
	inner := splitQuoted(top[2])
	if len(inner) < 3 || inner[0] != "herdr" || inner[1] != "--session" {
		return "", nil, fmt.Errorf("not routed through --session: %v", inner)
	}
	return inner[2], inner[3:], nil
}

func remoteHerdrArgs(t *testing.T, cmd string) (string, []string) {
	t.Helper()
	session, args, err := parseRemoteHerdrCmd(cmd)
	if err != nil {
		t.Fatalf("parsing %q: %v", cmd, err)
	}
	return session, args
}

// fakeSSH answers herdr commands run over a remoteRunner (one already-open
// SSH connection), so EnsureTabs can run without an instance.
//
// Replies are queued per verb ("tab list", "pane split") and the last one
// repeats, which is how a listing can differ before and after a create.
type fakeSSH struct {
	replies   map[string][]string
	fail      string     // verb to fail, e.g. "pane split"; empty means never
	failLimit int        // caps how many calls to fail verb fail; 0 means fail every time
	failed    int        // calls to fail verb failed so far
	calls     [][]string // argv after the session
	sessions  []string   // the session each call was addressed to
}

func (f *fakeSSH) Run(cmd string) (string, error) {
	session, rest, err := parseRemoteHerdrCmd(cmd)
	if err != nil {
		return "", err
	}
	f.sessions = append(f.sessions, session)
	f.calls = append(f.calls, rest)
	verb := rest[0] + " " + rest[1]
	if verb == f.fail && (f.failLimit == 0 || f.failed < f.failLimit) {
		f.failed++
		return "boom", fmt.Errorf("herdr %s failed", verb)
	}
	queue := f.replies[verb]
	if len(queue) == 0 {
		return "", nil
	}
	if len(queue) > 1 {
		f.replies[verb] = queue[1:]
	}
	return queue[0], nil
}

// ran returns every call of verb, each without the verb itself.
func (f *fakeSSH) ran(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) >= 2 && c[0]+" "+c[1] == verb {
			out = append(out, c[2:])
		}
	}
	return out
}

// herdr's `pane split --help` doesn't mark --direction as required, but a
// split with none is rejected live with a usage error -- confirmed by
// hitting it end to end, not from the docs. This pins the args so that
// regresses back to a build with no error, not a live one.
func TestPaneSplitCmd_AlwaysSetsADirection(t *testing.T) {
	_, args := remoteHerdrArgs(t, paneSplitCmd("auth", "w3:p1", checkout))
	if !strings.Contains(strings.Join(args, " "), "--direction down") {
		t.Errorf("paneSplitCmd() args = %v, want --direction down", args)
	}
}

func TestEnsureTabs_BuildsATabFromNothing(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list":   {onlyDefaultTab},
		"pane list":  {onlyRootPane},
		"tab create": {tabCreated},
		"pane split": {paneSplit},
	}}

	if err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}

	created := h.ran("tab create")
	if len(created) != 1 {
		t.Fatalf("tab creates = %v, want one", created)
	}
	got := strings.Join(created[0], " ")
	for _, want := range []string{"--workspace w3", "--label run", "--cwd " + checkout, "--no-focus"} {
		if !strings.Contains(got, want) {
			t.Errorf("tab create = %q, want %q", got, want)
		}
	}

	// The second pane splits off the first -- the tab's own root pane --
	// in its subdirectory, without taking focus.
	splits := h.ran("pane split")
	if len(splits) != 1 {
		t.Fatalf("splits = %v, want one", splits)
	}
	split := strings.Join(splits[0], " ")
	if !strings.HasPrefix(split, "w3:p2") || !strings.Contains(split, "--direction down") || !strings.Contains(split, "--cwd "+checkout+"/logs") || !strings.Contains(split, "--no-focus") {
		t.Errorf("split = %q, want w3:p2 split into %s/logs without focus", split, checkout)
	}

	renames := h.ran("pane rename")
	if len(renames) != 2 || strings.Join(renames[0], " ") != "w3:p2 server" || strings.Join(renames[1], " ") != "w3:p3 logs" {
		t.Errorf("renames = %v, want [w3:p2 server] [w3:p3 logs]", renames)
	}
	runs := h.ran("pane run")
	if len(runs) != 2 || strings.Join(runs[0], " ") != "w3:p2 npm run dev" || strings.Join(runs[1], " ") != "w3:p3 tail -f server.log" {
		t.Errorf("runs = %v, want each command in its own pane", runs)
	}
	for _, s := range h.sessions {
		if s != "auth" {
			t.Errorf("routed to session %q, want auth", s)
		}
	}
}

// Attaching again must add nothing and, above all, must not run
// `npm run dev` into a pane that is already running it.
func TestEnsureTabs_ReattachChangesNothing(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list": {`{"result":{"tabs":[{"label":"1","tab_id":"w3:t1"},{"label":"run","tab_id":"w3:t2"}]}}`},
		"pane list": {`{"result":{"panes":[{"pane_id":"w3:p1","tab_id":"w3:t1"},` +
			`{"label":"server","pane_id":"w3:p2","tab_id":"w3:t2"},` +
			`{"label":"logs","pane_id":"w3:p3","tab_id":"w3:t2"}]}}`},
	}}

	if err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	for _, verb := range []string{"tab create", "pane split", "pane rename", "pane run"} {
		if got := h.ran(verb); len(got) != 0 {
			t.Errorf("%s ran %v on a workspace that already has the layout", verb, got)
		}
	}
}

// A pane closed since the last attach comes back, split off the one before
// it, and only that pane's command runs.
func TestEnsureTabs_RestoresOnlyAMissingPane(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list": {`{"result":{"tabs":[{"label":"run","tab_id":"w3:t2"}]}}`},
		"pane list": {`{"result":{"panes":[` +
			`{"label":"server","pane_id":"w3:p2","tab_id":"w3:t2"}]}}`},
		"pane split": {paneSplit},
	}}

	if err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if got := h.ran("tab create"); len(got) != 0 {
		t.Errorf("recreated a tab that exists: %v", got)
	}
	splits := h.ran("pane split")
	if len(splits) != 1 || splits[0][0] != "w3:p2" {
		t.Errorf("splits = %v, want one split off the existing server pane", splits)
	}
	runs := h.ran("pane run")
	if len(runs) != 1 || runs[0][0] != "w3:p3" {
		t.Errorf("runs = %v, want only the restored logs pane's command", runs)
	}
}

// A pane with the same label in a different tab is not this tab's pane.
func TestEnsureTabs_MatchesPanesWithinTheirOwnTab(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list": {onlyDefaultTab},
		"pane list": {`{"result":{"panes":[` +
			`{"label":"server","pane_id":"w3:p1","tab_id":"w3:t1"}]}}`},
		"tab create": {tabCreated},
		"pane split": {paneSplit},
	}}

	if err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab}); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if runs := h.ran("pane run"); len(runs) != 2 {
		t.Errorf("runs = %v, want both panes of the new tab started", runs)
	}
}

// A tab with no panes is just a shell in the checkout; a pane with no
// command is just a labelled shell.
func TestEnsureTabs_RunsNothingThatWasNotAskedFor(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list":   {onlyDefaultTab},
		"pane list":  {onlyRootPane},
		"tab create": {tabCreated, tabCreated},
	}}
	tabs := []config.HerdrTab{
		{Label: "shell"},
		{Label: "edit", Panes: []config.HerdrPane{{Label: "code"}}},
	}

	if err := EnsureTabs(h, "auth", "w3", checkout, tabs); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if got := h.ran("tab create"); len(got) != 2 {
		t.Errorf("tab creates = %v, want two", got)
	}
	if got := h.ran("pane run"); len(got) != 0 {
		t.Errorf("ran %v with no command configured", got)
	}
	if got := h.ran("pane rename"); len(got) != 1 {
		t.Errorf("renames = %v, want just the code pane", got)
	}
}

func TestEnsureTabs_DoesNothingWithoutTabs(t *testing.T) {
	h := &fakeSSH{}
	if err := EnsureTabs(h, "auth", "w3", checkout, nil); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("ran %v with no layout configured -- herdrTabs is opt-in", h.calls)
	}
}

func TestEnsureTabs_ReportsAFailedSplit(t *testing.T) {
	h := &fakeSSH{
		replies: map[string][]string{
			"tab list":   {onlyDefaultTab},
			"pane list":  {onlyRootPane},
			"tab create": {tabCreated},
		},
		fail: "pane split",
	}
	err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab})
	if err == nil || !strings.Contains(err.Error(), "logs") {
		t.Errorf("error = %v, want one naming the logs pane", err)
	}
}

// A tab that keeps failing (a cwd that doesn't exist on the instance, say)
// must not stop every tab after it from being laid out, on this attach or
// any future one.
func TestEnsureTabs_ContinuesAfterATabFails(t *testing.T) {
	h := &fakeSSH{
		replies: map[string][]string{
			"tab list":   {onlyDefaultTab},
			"pane list":  {onlyRootPane},
			"tab create": {tabCreated}, // only the second attempt reaches the queue
		},
		fail:      "tab create",
		failLimit: 1,
	}
	tabs := []config.HerdrTab{
		{Label: "broken", Panes: []config.HerdrPane{{Label: "shell"}}},
		{Label: "run", Panes: []config.HerdrPane{
			{Label: "server", Command: strp("npm run dev")},
		}},
	}

	err := EnsureTabs(h, "auth", "w3", checkout, tabs)
	if err == nil {
		t.Fatal("EnsureTabs() error = nil, want the broken tab's failure reported")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %q, want it to name the broken tab", err)
	}
	if got := h.ran("tab create"); len(got) != 2 {
		t.Fatalf("tab creates = %v, want both tabs attempted", got)
	}
	if got := h.ran("pane run"); len(got) != 1 || !strings.Contains(strings.Join(got[0], " "), "npm run dev") {
		t.Errorf("runs = %v, want the run tab still laid out after broken failed", got)
	}
}

func TestEnsureTabs_ReportsATabListFailure(t *testing.T) {
	h := &fakeSSH{fail: "tab list"}
	if err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab}); err == nil {
		t.Fatal("EnsureTabs() error = nil when the tab list failed")
	}
}

func TestPaneDir_IsRelativeToTheCheckout(t *testing.T) {
	for _, tt := range []struct {
		cwd  *string
		want string
	}{
		{nil, checkout},
		{strp(""), checkout},
		{strp("logs"), checkout + "/logs"},
		{strp("a/../b"), checkout + "/b"},
		{strp("/var/log"), "/var/log"},
		{strp("~"), "~"},
		{strp("~/x"), "~/x"},
	} {
		if got := paneDir(checkout, config.HerdrPane{Cwd: tt.cwd}); got != tt.want {
			t.Errorf("paneDir(%v) = %q, want %q", tt.cwd, got, tt.want)
		}
	}
}

// Duplicate tab labels in the merged config: first wins, second is skipped.
func TestEnsureTabs_SkipsDuplicateTabLabels(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list":   {onlyDefaultTab},
		"pane list":  {onlyRootPane},
		"tab create": {tabCreated, tabCreated}, // Reply for both tab create attempts
	}}
	tabs := []config.HerdrTab{
		{Label: "run", Panes: []config.HerdrPane{
			{Label: "server", Command: strp("npm run dev")},
		}},
		{Label: "run", Panes: []config.HerdrPane{ // Duplicate label
			{Label: "other", Command: strp("echo dup")},
		}},
	}

	if err := EnsureTabs(h, "auth", "w3", checkout, tabs); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	creates := h.ran("tab create")
	if len(creates) != 1 {
		t.Fatalf("tab creates = %v, want one (duplicate label skipped)", creates)
	}
	runs := h.ran("pane run")
	if len(runs) != 1 || strings.Join(runs[0], " ") != "w3:p2 npm run dev" {
		t.Errorf("runs = %v, want only the first tab's command", runs)
	}
}

// Duplicate pane labels within one tab: first wins, second is skipped.
func TestEnsureTabs_SkipsDuplicatePaneLabels(t *testing.T) {
	h := &fakeSSH{replies: map[string][]string{
		"tab list":   {onlyDefaultTab},
		"pane list":  {onlyRootPane},
		"tab create": {tabCreated},
		"pane split": {paneSplit},
	}}
	tabs := []config.HerdrTab{
		{Label: "run", Panes: []config.HerdrPane{
			{Label: "logs", Command: strp("tail -f log1.log")},
			{Label: "logs", Command: strp("tail -f log2.log")}, // Duplicate label
		}},
	}

	if err := EnsureTabs(h, "auth", "w3", checkout, tabs); err != nil {
		t.Fatalf("EnsureTabs() error = %v", err)
	}
	renames := h.ran("pane rename")
	if len(renames) != 1 || strings.Join(renames[0], " ") != "w3:p2 logs" {
		t.Errorf("renames = %v, want one (duplicate label skipped)", renames)
	}
	runs := h.ran("pane run")
	if len(runs) != 1 || strings.Join(runs[0], " ") != "w3:p2 tail -f log1.log" {
		t.Errorf("runs = %v, want only the first pane's command", runs)
	}
}

// Pane run failure: error reported, but pane was already renamed to pin the label.
func TestEnsureTabs_ReportsRunFailureButKeepsRename(t *testing.T) {
	h := &fakeSSH{
		replies: map[string][]string{
			"tab list":   {onlyDefaultTab},
			"pane list":  {onlyRootPane},
			"tab create": {tabCreated},
		},
		fail: "pane run",
	}

	err := EnsureTabs(h, "auth", "w3", checkout, []config.HerdrTab{runTab})
	if err == nil {
		t.Fatal("EnsureTabs() error = nil when pane run failed")
	}
	if !strings.Contains(err.Error(), "npm run dev") || !strings.Contains(err.Error(), "server") {
		t.Errorf("error = %q, want it to name the command and pane label", err)
	}
	renames := h.ran("pane rename")
	if len(renames) == 0 {
		t.Error("pane was not renamed before run failed (order violation)")
	}
}
