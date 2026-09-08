package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseServeStatus_Empty(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "serve-status-empty.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	got, err := parseServeStatus(string(raw))
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseServeStatus(%q) = %+v, want none", "{}", got)
	}
}

func TestParseServeStatus_OneTCPEntry(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "serve-status-tcp.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	got, err := parseServeStatus(string(raw))
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseServeStatus() returned %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Port != 9876 {
		t.Errorf("Port = %d, want 9876", got[0].Port)
	}
	if got[0].Forward != "localhost:9876" {
		t.Errorf("Forward = %q, want %q", got[0].Forward, "localhost:9876")
	}
}

func TestParseServeStatus_SortedByPort(t *testing.T) {
	// Map iteration order is random in Go, so without an explicit sort
	// the picker would renumber its choices between runs.
	in := `{"TCP":{"9877":{"TCPForward":"localhost:9877"},"9876":{"TCPForward":"localhost:9876"}}}`
	got, err := parseServeStatus(in)
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v", err)
	}
	if len(got) != 2 || got[0].Port != 9876 || got[1].Port != 9877 {
		t.Errorf("parseServeStatus() = %+v, want ports ascending [9876 9877]", got)
	}
}

func TestParseServeStatus_Garbage(t *testing.T) {
	if _, err := parseServeStatus("not json"); err == nil {
		t.Error("parseServeStatus(\"not json\") error = nil, want an error")
	}
}

func TestParseServeStatus_LeadingStderrNoise(t *testing.T) {
	// client.Run returns combined stdout+stderr, so a routine warning
	// (sudo's hostname resolution complaint under bash -lc, here) that
	// doesn't change the exit status must not make this fail.
	in := "sudo: unable to resolve host cloudlab-1: Name or service not known\n" +
		`{"TCP":{"9876":{"TCPForward":"localhost:9876"}}}`
	got, err := parseServeStatus(in)
	if err != nil {
		t.Fatalf("parseServeStatus() error = %v, want the noise skipped", err)
	}
	if len(got) != 1 || got[0].Port != 9876 {
		t.Errorf("parseServeStatus() = %+v, want one entry on 9876", got)
	}
}

func TestServeArgs(t *testing.T) {
	got := serveArgs("/usr/bin/tailscale", 8888)
	for _, want := range []string{"sudo", "/usr/bin/tailscale", "serve", "--bg", "--tcp 8888", "tcp://localhost:8888"} {
		if !strings.Contains(got, want) {
			t.Errorf("serveArgs() = %q, missing %q", got, want)
		}
	}
}

func TestUnserveArgs(t *testing.T) {
	got := unserveArgs("/usr/bin/tailscale", 8888)
	if !strings.Contains(got, "--tcp 8888 off") {
		t.Errorf("unserveArgs() = %q, want it to end the entry with `--tcp 8888 off`", got)
	}
	// reset clears entries the user created by hand; it must never appear.
	if strings.Contains(got, "reset") {
		t.Errorf("unserveArgs() = %q, must not use `serve reset`", got)
	}
}

func TestServe_RunsTailscaleServe(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got []string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		got = append(got, cmd)
		if strings.Contains(cmd, "command -v tailscale") {
			return "/usr/bin/tailscale\n", 0
		}
		return "", 0
	})

	if err := Serve(context.Background(), addr, "devuser", 8888); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "serve --bg --tcp 8888 tcp://localhost:8888") {
		t.Errorf("Serve() ran %q, want the --bg --tcp publish command", joined)
	}
}

func TestUnserve_TurnsOffOneEntry(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	var got []string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		got = append(got, cmd)
		if strings.Contains(cmd, "command -v tailscale") {
			return "/usr/bin/tailscale\n", 0
		}
		return "", 0
	})

	if err := Unserve(context.Background(), addr, "devuser", 8888); err != nil {
		t.Fatalf("Unserve() error = %v", err)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "--tcp 8888 off") {
		t.Errorf("Unserve() ran %q, want the per-entry off command", joined)
	}
	if strings.Contains(joined, "serve reset") {
		t.Errorf("Unserve() ran %q, which would clear entries cloudlab did not create", joined)
	}
}

func TestServeStatus_ParsesRemoteJSON(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	raw, err := os.ReadFile(filepath.Join("testdata", "serve-status-tcp.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var got []string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		got = append(got, cmd)
		if strings.Contains(cmd, "command -v tailscale") {
			return "/usr/bin/tailscale\n", 0
		}
		return string(raw), 0
	})

	entries, err := ServeStatus(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("ServeStatus() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Port != 9876 {
		t.Errorf("ServeStatus() = %+v, want one entry on 9876", entries)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "sudo") || !strings.Contains(joined, "/usr/bin/tailscale") || !strings.Contains(joined, "serve status --json") {
		t.Errorf("ServeStatus() ran %q, want the sudo status --json invocation", joined)
	}
}
