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
