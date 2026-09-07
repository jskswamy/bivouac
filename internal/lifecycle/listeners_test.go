package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseListeners_RealSSOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ss-tlnp.txt"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	got := parseListeners(string(raw))

	want := []Listener{
		{Addr: "127.0.0.53%lo", Port: 53},
		{Addr: "0.0.0.0", Port: 22},
		{Addr: "127.0.0.1", Port: 24543, Process: "moshi-hook"},
		{Addr: "100.81.106.84", Port: 35669},
		{Addr: "127.0.0.54", Port: 53},
		{Addr: "fd7a:115c:a1e0::db38:6a55", Port: 52076},
		{Addr: "::", Port: 22},
	}
	if len(got) != len(want) {
		t.Fatalf("parseListeners() returned %d listeners, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listener %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseListeners_SkipsHeader(t *testing.T) {
	got := parseListeners("State  Recv-Q Send-Q Local Address:Port  Peer Address:Port\n")
	if len(got) != 0 {
		t.Errorf("parseListeners() = %+v, want none — the header is not a listener", got)
	}
}

func TestListeners_RunsSSAndParses(t *testing.T) {
	startFakeAgent(t)
	t.Setenv("HOME", t.TempDir())

	raw, err := os.ReadFile(filepath.Join("testdata", "ss-tlnp.txt"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	var gotCmd string
	addr := startFakeSSHServer(t, func(cmd string, _ []byte) (string, uint32) {
		gotCmd = cmd
		return string(raw), 0
	})

	got, err := Listeners(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Listeners() error = %v", err)
	}
	if !strings.Contains(gotCmd, "ss -tlnp") {
		t.Errorf("Listeners() ran %q, want it to invoke `ss -tlnp`", gotCmd)
	}
	if len(got) != 7 {
		t.Errorf("Listeners() returned %d listeners, want 7", len(got))
	}
}

func TestListener_LoopbackOnly(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53%lo", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"::", false},
		{"100.81.106.84", false},
	}
	for _, c := range cases {
		if got := (Listener{Addr: c.addr}).LoopbackOnly(); got != c.want {
			t.Errorf("Listener{Addr: %q}.LoopbackOnly() = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestListener_Infrastructure(t *testing.T) {
	cases := []struct {
		name string
		l    Listener
		want bool
	}{
		{"sshd", Listener{Addr: "0.0.0.0", Port: 22}, true},
		{"sshd v6", Listener{Addr: "::", Port: 22}, true},
		{"systemd-resolved", Listener{Addr: "127.0.0.53%lo", Port: 53}, true},
		{"tailscaled by bind address", Listener{Addr: "100.81.106.84", Port: 35669}, true},
		{"tailscaled v6", Listener{Addr: "fd7a:115c:a1e0::db38:6a55", Port: 52076}, true},
		{"a user's loopback service", Listener{Addr: "127.0.0.1", Port: 8888}, false},
		{"a user's routable service", Listener{Addr: "0.0.0.0", Port: 3000}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.l.Infrastructure(); got != c.want {
				t.Errorf("Infrastructure() = %v, want %v", got, c.want)
			}
		})
	}
}
