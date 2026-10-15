package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/testenv"
)

const terminalOwnershipScheme = "herdr-workspace-root-terminal-v1"

type herdrCapabilityWorkspace struct {
	ID         string
	Label      string
	RootPaneID string
	TerminalID string
}

type herdrWorkspaceReply struct {
	Result struct {
		Workspace struct {
			ID    string `json:"workspace_id"`
			Label string `json:"label"`
		} `json:"workspace"`
		RootPane struct {
			ID         string `json:"pane_id"`
			TerminalID string `json:"terminal_id"`
		} `json:"root_pane"`
	} `json:"result"`
}

type herdrWorkspaceListReply struct {
	Result struct {
		Workspaces []struct {
			ID    string `json:"workspace_id"`
			Label string `json:"label"`
		} `json:"workspaces"`
	} `json:"result"`
}

type herdrPaneListReply struct {
	Result struct {
		Panes []struct {
			ID         string `json:"pane_id"`
			TerminalID string `json:"terminal_id"`
		} `json:"panes"`
	} `json:"result"`
}

// These tests are an opt-in capability gate, not a mock of Herdr. They pin the
// evidence the ownership adapter may rely on before production cleanup exists.
func TestHerdrRetirementCapabilities(t *testing.T) {
	binary := os.Getenv("BIVOUAC_HERDR_BIN")
	if binary == "" {
		t.Skip("set BIVOUAC_HERDR_BIN to Herdr 0.9.4 for retirement capability tests")
	}

	first := testenv.StartHerdr(t, binary)
	second := testenv.StartHerdr(t, binary)
	intent := "bivouac-intent-7a9d92c60a6a"

	// Discard the create reply to model an applied request whose response was
	// lost. Recovery is deliberately limited to the unique persisted intent
	// label; the durable proof recorded after recovery is the opaque terminal.
	if _, err := first.Run("workspace", "create", "--cwd", first.Home, "--label", intent, "--no-focus"); err != nil {
		t.Fatal(err)
	}
	owned := recoverCapabilityWorkspace(t, first, intent)
	if owned.ID == "" || owned.TerminalID == "" {
		t.Fatalf("recovered incomplete %s proof: %+v", terminalOwnershipScheme, owned)
	}

	foreign := createCapabilityWorkspace(t, second, second.Home, "other")
	if owned.ID != foreign.ID {
		t.Fatalf("fixtures did not produce colliding server-scoped ids: %q != %q", owned.ID, foreign.ID)
	}
	if _, err := first.Run("workspace", "rename", owned.ID, "friendly"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Run("workspace", "rename", foreign.ID, "friendly"); err != nil {
		t.Fatal(err)
	}
	if !capabilityProofMatches(t, first, owned) {
		t.Fatal("rename invalidated the creating server's terminal proof")
	}
	if capabilityProofMatches(t, second, owned) {
		t.Fatal("a friendly label or colliding workspace id authorised a foreign workspace")
	}

	sentinel := filepath.Join(first.Home, "closed")
	command := "trap 'printf closed > " + shellQuote(sentinel) + "' EXIT HUP TERM; while :; do sleep 1; done"
	if out, err := first.Run("pane", "run", owned.RootPaneID, command); err != nil {
		t.Fatalf("start sentinel process: %v: %s", err, out)
	}
	closeCapabilityWorkspace(t, first, owned)
	waitForCapabilityFile(t, sentinel)
	if capabilityWorkspaceExists(t, first, owned.ID) {
		t.Fatal("owned workspace still exists after close")
	}
	if !capabilityWorkspaceExists(t, second, foreign.ID) {
		t.Fatal("closing the owned workspace affected the other server")
	}
}

func TestHerdrOwnershipAfterRestart(t *testing.T) {
	binary := os.Getenv("BIVOUAC_HERDR_BIN")
	if binary == "" {
		t.Skip("set BIVOUAC_HERDR_BIN to Herdr 0.9.4 for retirement capability tests")
	}

	fixture := testenv.StartHerdr(t, binary)
	owned := createCapabilityWorkspace(t, fixture, fixture.Home, "owned")
	if err := fixture.Restart(); err != nil {
		t.Fatal(err)
	}
	panes := capabilityPanes(t, fixture, owned.ID)
	if len(panes.Result.Panes) != 1 {
		t.Fatalf("restored workspace %s has %d panes", owned.ID, len(panes.Result.Panes))
	}
	replacement := panes.Result.Panes[0]
	if replacement.ID != owned.RootPaneID {
		t.Fatalf("restart did not reuse the public pane handle: replacement=%q, original=%q", replacement.ID, owned.RootPaneID)
	}
	if replacement.TerminalID == owned.TerminalID {
		t.Fatalf("replacement process reused opaque terminal identity %q", owned.TerminalID)
	}
	if err := closeCapabilityWorkspaceChecked(fixture, owned); err == nil || !strings.Contains(err.Error(), "ownership proof") {
		t.Fatalf("closing replacement with stale proof error = %v, want ownership refusal", err)
	}
	if !capabilityWorkspaceExists(t, fixture, owned.ID) {
		t.Fatal("stale ownership proof deleted the restored replacement workspace")
	}
}

func createCapabilityWorkspace(t *testing.T, fixture *testenv.HerdrFixture, cwd, label string) herdrCapabilityWorkspace {
	t.Helper()
	out, err := fixture.Run("workspace", "create", "--cwd", cwd, "--label", label, "--no-focus")
	if err != nil {
		t.Fatalf("create workspace: %v: %s", err, out)
	}
	var reply herdrWorkspaceReply
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatalf("decode create reply: %v: %s", err, out)
	}
	return herdrCapabilityWorkspace{
		ID:         reply.Result.Workspace.ID,
		Label:      reply.Result.Workspace.Label,
		RootPaneID: reply.Result.RootPane.ID,
		TerminalID: reply.Result.RootPane.TerminalID,
	}
}

func recoverCapabilityWorkspace(t *testing.T, fixture *testenv.HerdrFixture, intent string) herdrCapabilityWorkspace {
	t.Helper()
	out, err := fixture.Run("workspace", "list")
	if err != nil {
		t.Fatalf("list workspaces: %v: %s", err, out)
	}
	var reply herdrWorkspaceListReply
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatalf("decode workspace list: %v: %s", err, out)
	}
	var matches []string
	for _, workspace := range reply.Result.Workspaces {
		if workspace.Label == intent {
			matches = append(matches, workspace.ID)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("intent %q matched workspace ids %v", intent, matches)
	}
	panes := capabilityPanes(t, fixture, matches[0])
	if len(panes.Result.Panes) != 1 {
		t.Fatalf("new intent workspace %s has %d panes", matches[0], len(panes.Result.Panes))
	}
	pane := panes.Result.Panes[0]
	return herdrCapabilityWorkspace{ID: matches[0], Label: intent, RootPaneID: pane.ID, TerminalID: pane.TerminalID}
}

func capabilityProofMatches(t *testing.T, fixture *testenv.HerdrFixture, owned herdrCapabilityWorkspace) bool {
	t.Helper()
	out, err := fixture.Run("workspace", "get", owned.ID)
	if err != nil || !strings.Contains(out, `"workspace_id":"`+owned.ID+`"`) {
		return false
	}
	panes := capabilityPanes(t, fixture, owned.ID)
	for _, pane := range panes.Result.Panes {
		if pane.ID == owned.RootPaneID && pane.TerminalID == owned.TerminalID {
			return true
		}
	}
	return false
}

func capabilityPanes(t *testing.T, fixture *testenv.HerdrFixture, workspace string) herdrPaneListReply {
	t.Helper()
	out, err := fixture.Run("pane", "list", "--workspace", workspace)
	if err != nil {
		t.Fatalf("list panes in %s: %v: %s", workspace, err, out)
	}
	var reply herdrPaneListReply
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatalf("decode pane list: %v: %s", err, out)
	}
	return reply
}

func closeCapabilityWorkspace(t *testing.T, fixture *testenv.HerdrFixture, owned herdrCapabilityWorkspace) {
	t.Helper()
	if err := closeCapabilityWorkspaceChecked(fixture, owned); err != nil {
		t.Fatal(err)
	}
}

func closeCapabilityWorkspaceChecked(fixture *testenv.HerdrFixture, owned herdrCapabilityWorkspace) error {
	out, err := fixture.Run("pane", "list", "--workspace", owned.ID)
	if err != nil {
		return fmt.Errorf("observe workspace before close: %w: %s", err, out)
	}
	var panes herdrPaneListReply
	if err := json.Unmarshal([]byte(out), &panes); err != nil {
		return fmt.Errorf("decode workspace ownership proof: %w", err)
	}
	matched := false
	for _, pane := range panes.Result.Panes {
		matched = matched || pane.ID == owned.RootPaneID && pane.TerminalID == owned.TerminalID
	}
	if !matched {
		return fmt.Errorf("workspace %s no longer matches its %s ownership proof", owned.ID, terminalOwnershipScheme)
	}
	if out, err := fixture.Run("workspace", "close", owned.ID); err != nil {
		return fmt.Errorf("close workspace: %w: %s", err, out)
	}
	return nil
}

func capabilityWorkspaceExists(t *testing.T, fixture *testenv.HerdrFixture, workspace string) bool {
	t.Helper()
	_, err := fixture.Run("workspace", "get", workspace)
	return err == nil
}

func waitForCapabilityFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && string(data) == "closed" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("pane shutdown did not write %s", path)
}
