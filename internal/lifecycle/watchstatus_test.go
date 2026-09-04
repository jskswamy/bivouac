package lifecycle

import (
	"context"
	"os/exec"
	"testing"
)

func TestMutagenListArgs_BuildsExpectedCommand(t *testing.T) {
	got := mutagenListArgs("myrepo")
	want := []string{"sync", "list", "myrepo", "--template={{range .}}{{.Status}}|{{.Alpha.Connected}}|{{.Beta.Connected}}|{{len .Conflicts}}|{{.LastError}}{{end}}"}
	if len(got) != len(want) {
		t.Fatalf("mutagenListArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mutagenListArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseWatchStatus_ParsesAllFields(t *testing.T) {
	got, err := parseWatchStatus("Watching|true|false|2|scan failed")
	if err != nil {
		t.Fatalf("parseWatchStatus() error = %v", err)
	}
	want := WatchStatus{Running: true, Status: "Watching", AlphaConnected: true, BetaConnected: false, Conflicts: 2, LastError: "scan failed"}
	if got != want {
		t.Errorf("parseWatchStatus() = %+v, want %+v", got, want)
	}
}

func TestParseWatchStatus_MalformedLine_ReturnsError(t *testing.T) {
	if _, err := parseWatchStatus("not enough fields"); err == nil {
		t.Fatal("parseWatchStatus() error = nil, want an error for a malformed line")
	}
}

func TestGetWatchStatus_NoSession_ReturnsNotRunningNoError(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}
	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	got, err := GetWatchStatus(context.Background(), "cloudlab-watchstatus-test-nonexistent")
	if err != nil {
		t.Fatalf("GetWatchStatus() error = %v, want nil for a nonexistent session", err)
	}
	if got.Running {
		t.Errorf("GetWatchStatus().Running = true, want false for a nonexistent session")
	}
}

func TestGetWatchStatus_RealSession_ReturnsRunning(t *testing.T) {
	if _, err := exec.LookPath("mutagen"); err != nil {
		t.Skip("mutagen not on PATH")
	}
	t.Setenv("MUTAGEN_DATA_DIRECTORY", t.TempDir())

	src := t.TempDir()
	dst := t.TempDir()
	name := "cloudlab-watchstatus-test-real"
	create := exec.Command("mutagen", "sync", "create", "--name="+name, src, dst)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("mutagen sync create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("mutagen", "sync", "terminate", name).Run()
		_ = exec.Command("mutagen", "daemon", "stop").Run()
	})

	got, err := GetWatchStatus(context.Background(), name)
	if err != nil {
		t.Fatalf("GetWatchStatus() error = %v", err)
	}
	if !got.Running {
		t.Error("GetWatchStatus().Running = false, want true for a real session")
	}
	if got.Status == "" {
		t.Error("GetWatchStatus().Status is empty, want a real Mutagen status string")
	}
}
