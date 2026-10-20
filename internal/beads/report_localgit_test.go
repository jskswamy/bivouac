package beads

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type localReportRunner struct{}

func (localReportRunner) Run(cmd string) (string, error) {
	// #nosec G204 -- test-only, runs the actual instance command builders.
	out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	return string(out), err
}

func TestReportRoundTripKeepsPinnedIssueTimestamps(t *testing.T) {
	requireBd(t)
	root := t.TempDir()
	mac, remote := filepath.Join(root, "mac"), filepath.Join(root, "remote")
	initGitRepo(t, mac)
	initGitRepo(t, remote)
	mustRun(t, mac, "bd", "init", "--stealth")
	mustRun(t, mac, "bd", "create", "Before session", "--id", "mac-one")
	if err := Seed(t.Context(), mac, "report", FileURL(remote)); err != nil {
		t.Fatal(err)
	}
	mustRun(t, remote, "bd", "init", "--stealth", "--remote", FileURL(remote))
	runner := localReportRunner{}
	start, err := InstanceCommit(runner, remote)
	if err != nil {
		t.Fatal(err)
	}
	local, err := LocalIssues(t.Context(), mac)
	if err != nil {
		t.Fatal(err)
	}
	// bd timestamps have second precision; force an observably later edit.
	time.Sleep(time.Until(local["mac-one"].UpdatedAt.Add(time.Second)))
	mustRun(t, remote, "bd", "update", "mac-one", "--title", "Pinned change")
	var expected []Issue
	if err := json.Unmarshal([]byte(mustRun(t, remote, "bd", "show", "mac-one", "--json")), &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected) != 1 || expected[0].UpdatedAt.IsZero() {
		t.Fatalf("bd show returned no timestamp: %+v", expected)
	}
	pinned, err := InstanceCommit(runner, remote)
	if err != nil {
		t.Fatal(err)
	}
	// A later edit must not turn the pinned receipt into a moving target.
	mustRun(t, remote, "bd", "update", "mac-one", "--title", "After pin")
	changes, err := SessionChanges(runner, remote, start, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Issue.Title != "Pinned change" ||
		!changes[0].Issue.UpdatedAt.Equal(expected[0].UpdatedAt) {
		t.Fatalf("want pinned title and timestamp, got %+v", changes)
	}
	if got := CheckSynced(changes, local, nil); got[0].Sync != NotSynced {
		t.Fatalf("unpulled edit reported %s", got[0].Sync)
	}
	if _, err := Pull(t.Context(), mac, "report", runner, remote); err != nil {
		t.Fatal(err)
	}
	local, err = LocalIssues(t.Context(), mac)
	if err != nil {
		t.Fatal(err)
	}
	if local["mac-one"].Title != "After pin" {
		t.Fatalf("pull lost latest edit: %+v", local["mac-one"])
	}
	if got := CheckSynced(changes, local, nil); got[0].Sync != Synced {
		t.Fatalf("pulled issue reported %s", got[0].Sync)
	}
}
