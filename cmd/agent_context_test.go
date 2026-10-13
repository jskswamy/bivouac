package cmd

import (
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/state"
)

func writeOneRecordFixture(t *testing.T) {
	t.Helper()
	writeRecordsFixture(t, state.Record{Name: "myinstance", Provider: "digitalocean", IP: "192.0.2.10"})
}

func TestRunningUnderAgentRecognizesKnownMarkers(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	if !runningUnderAgent() {
		t.Fatal("CLAUDECODE=1 not detected")
	}
}

func TestAgentMarkerDoesNotSwitchListToJSON(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	writeOneRecordFixture(t)
	out := executeRoot(t, "list")
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("list defaulted to JSON under agent: %s", out)
	}
	if !strings.Contains(out, "NAME") {
		t.Fatalf("list did not print human table: %s", out)
	}
}

func TestRootHelpShowsAgentJSONHintUnderAgent(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	out := executeRoot(t, "--help")
	if !strings.Contains(out, agentJSONHint()) {
		t.Fatalf("root help missing agent JSON hint: %s", out)
	}
}
