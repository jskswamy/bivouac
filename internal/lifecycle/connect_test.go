package lifecycle

import "testing"

func TestConnectTarget(t *testing.T) {
	cases := []struct {
		name        string
		tailnetIP   string
		listener    Listener
		wantURL     string
		wantForward bool
	}{
		{
			name:        "routable service on a tailnet instance",
			tailnetIP:   "100.81.106.84",
			listener:    Listener{Addr: "0.0.0.0", Port: 8888},
			wantURL:     "http://100.81.106.84:8888",
			wantForward: false,
		},
		{
			name:        "loopback service on a tailnet instance still needs a forward",
			tailnetIP:   "100.81.106.84",
			listener:    Listener{Addr: "127.0.0.1", Port: 8888},
			wantURL:     "http://localhost:8888",
			wantForward: true,
		},
		{
			name:        "no tailnet means forward regardless",
			tailnetIP:   "",
			listener:    Listener{Addr: "0.0.0.0", Port: 3000},
			wantURL:     "http://localhost:3000",
			wantForward: true,
		},
		{
			name:        "no tailnet and loopback both force a forward",
			tailnetIP:   "",
			listener:    Listener{Addr: "127.0.0.1", Port: 8888},
			wantURL:     "http://localhost:8888",
			wantForward: true,
		},
		{
			name:        "ipv6 wildcard is routable",
			tailnetIP:   "100.81.106.84",
			listener:    Listener{Addr: "::", Port: 3000},
			wantURL:     "http://100.81.106.84:3000",
			wantForward: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, forward := ConnectTarget(c.tailnetIP, c.listener)
			if url != c.wantURL {
				t.Errorf("url = %q, want %q", url, c.wantURL)
			}
			if forward != c.wantForward {
				t.Errorf("mustForward = %v, want %v", forward, c.wantForward)
			}
		})
	}
}

// forwardArgs is Forward's pure argv builder, extracted for the same reason
// sshArgs/tmuxArgs/herdrArgs are: the decision (which flags, in what shape)
// is what is worth testing, while Forward itself execs a real ssh binary.
func TestForwardArgs(t *testing.T) {
	got := forwardArgs("203.0.113.5", "devuser", 8888)
	// The 127.0.0.1 prefix is load-bearing, not cosmetic: without it ssh
	// binds every address family and keeps running when only one
	// collides, which defeats ExitOnForwardFailure entirely.
	want := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", "127.0.0.1:8888:localhost:8888", "devuser@203.0.113.5"}
	if len(got) != len(want) {
		t.Fatalf("forwardArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("forwardArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConnectTarget_NonHTTPPortGetsNoScheme(t *testing.T) {
	// http://host:22 is what made curl read sshd's version banner as an
	// HTTP/0.9 response. A bare host:port says nothing untrue.
	url, forward := ConnectTarget("100.81.106.84", Listener{Addr: "0.0.0.0", Port: 22})
	if url != "100.81.106.84:22" {
		t.Errorf("url = %q, want %q — no scheme for a port that does not speak HTTP", url, "100.81.106.84:22")
	}
	if forward {
		t.Error("mustForward = true, want false — 0.0.0.0 is routable")
	}
}

func TestVisibleListeners(t *testing.T) {
	// The real fixture's shape: seven sockets, one of them the user's.
	all := []Listener{
		{Addr: "127.0.0.53%lo", Port: 53},
		{Addr: "0.0.0.0", Port: 22},
		{Addr: "127.0.0.1", Port: 24543, Process: "moshi-hook"},
		{Addr: "100.81.106.84", Port: 35669},
		{Addr: "127.0.0.54", Port: 53},
		{Addr: "fd7a:115c:a1e0::db38:6a55", Port: 52076},
		{Addr: "::", Port: 22},
	}

	visible := VisibleListeners(all, false)
	if len(visible) != 1 || visible[0].Port != 24543 {
		t.Errorf("VisibleListeners(all=false) = %+v, want only the moshi-hook socket on 24543", visible)
	}

	if got := VisibleListeners(all, true); len(got) != len(all) {
		t.Errorf("VisibleListeners(all=true) returned %d, want all %d", len(got), len(all))
	}
}
