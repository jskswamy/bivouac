package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/state"
)

func writeRecordsFixture(t *testing.T, records ...state.Record) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := store.Put(record); err != nil {
			t.Fatal(err)
		}
	}
}

func executeRoot(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestListJSON_PrintsInstances(t *testing.T) {
	writeRecordsFixture(t,
		state.Record{Name: "alpha", Provider: "digitalocean", IP: "192.0.2.10"},
		state.Record{Name: "beta", Provider: "digitalocean", IP: "192.0.2.11"},
	)

	out := executeRoot(t, "list", "--json")
	var got struct {
		Instances []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			IP       string `json:"ip"`
		} `json:"instances"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Instances) != 2 {
		t.Fatalf("instances = %d", len(got.Instances))
	}
	if strings.Contains(out, "NAME") {
		t.Fatalf("json output contains table header: %s", out)
	}
}

func TestListJSON_EmptyState(t *testing.T) {
	writeRecordsFixture(t)

	out := executeRoot(t, "list", "--json")
	var got struct {
		Instances []struct{} `json:"instances"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Instances) != 0 {
		t.Fatalf("instances = %d", len(got.Instances))
	}
	if strings.Contains(out, "no instances") {
		t.Fatalf("json output contains human empty message: %s", out)
	}
}

func TestSessionListJSON_PrintsSessions(t *testing.T) {
	record := state.Record{Name: "myinstance", IP: "192.0.2.10", User: "devuser"}
	record.PutSession(state.Session{Name: "alpha", LocalRepo: t.TempDir(), Base: "abc123"})
	writeRecordsFixture(t, record)

	out := executeRoot(t, "session", "list", "--json")
	var got struct {
		Sessions []struct {
			Name           string `json:"name"`
			Instance       string `json:"instance"`
			Branch         string `json:"branch"`
			WorktreeExists bool   `json:"worktreeExists"`
			WorktreeDirty  bool   `json:"worktreeDirty"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions = %d", len(got.Sessions))
	}
	if got.Sessions[0].Name != "alpha" || got.Sessions[0].Instance != "myinstance" {
		t.Fatalf("session = %+v", got.Sessions[0])
	}
	if strings.Contains(out, "NAME") {
		t.Fatalf("json output contains table header: %s", out)
	}
}

func TestStatusJSON_DegradesWithoutProviderToken(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	record := state.Record{Name: "myinstance", IP: "192.0.2.10", User: "devuser", Provider: "digitalocean"}
	record.PutSession(state.Session{Name: "alpha", LocalRepo: t.TempDir(), Base: "abc123"})
	writeRecordsFixture(t, record)

	out := executeRoot(t, "status", "myinstance", "--json")
	var got struct {
		Instance struct {
			Name       string `json:"name"`
			IP         string `json:"ip"`
			LiveStatus string `json:"liveStatus"`
			LiveError  string `json:"liveError,omitempty"`
		} `json:"instance"`
		Sessions []struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if got.Instance.Name != "myinstance" {
		t.Fatalf("instance name = %q", got.Instance.Name)
	}
	if got.Instance.LiveStatus != "unknown" {
		t.Fatalf("liveStatus = %q", got.Instance.LiveStatus)
	}
	if strings.Contains(out, "Provider") || strings.Contains(out, "SESSIONS") {
		t.Fatalf("json output contains human labels: %s", out)
	}
}
