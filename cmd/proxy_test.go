package cmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/iap"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
)

func newMockProxyIAPServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()

		// Send CONNECT_SUCCESS
		sid := []byte("session-id")
		hdr := make([]byte, 6+len(sid))
		binary.BigEndian.PutUint16(hdr[0:2], iap.TagConnectSuccess)
		binary.BigEndian.PutUint32(hdr[2:6], uint32(len(sid)))
		copy(hdr[6:], sid)
		_ = c.Write(r.Context(), websocket.MessageBinary, hdr)

		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary && len(data) >= 6 {
				tag := binary.BigEndian.Uint16(data[0:2])
				if tag == iap.TagData {
					// echo back and close
					_ = c.Write(r.Context(), websocket.MessageBinary, data)
					_ = c.Close(websocket.StatusNormalClosure, "done")
					return
				}
			}
		}
	}))
	return s
}

func TestProxyCommand_BridgesIO(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	store, err := state.Open()
	if err != nil {
		t.Fatalf("state.Open() err = %v", err)
	}

	rec := state.Record{
		Name:     "myvm",
		Provider: "gcloud",
		Project:  "test-project",
		Region:   "us-central1-a",
		IP:       "10.0.0.5",
	}
	if err := store.Put(rec); err != nil {
		t.Fatalf("store.Put() err = %v", err)
	}

	srv := newMockProxyIAPServer(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v4/connect"
	t.Setenv("GCLOUD_IAP_URL", wsURL)
	t.Setenv("GCLOUD_ACCESS_TOKEN", "mock-token")

	input := "SSH-2.0-OpenSSH_9.0\r\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	root := newRootCmd()
	root.SetArgs([]string{"proxy", "myvm"})
	root.SetIn(in)
	root.SetOut(&out)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = root.ExecuteContext(ctx)

	if !strings.Contains(out.String(), "SSH-2.0-OpenSSH_9.0") {
		t.Errorf("output = %q, want it to contain echo %q", out.String(), input)
	}
}

func TestProxyCommand_PrefersReachableTailscaleEndpoint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "SSH-2.0-TailscaleOverlay\r\n")
	}()

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	rec := state.Record{
		Name:            "myvm",
		Provider:        "gcloud",
		Project:         "test-project",
		Region:          "us-central1-a",
		IP:              "10.0.0.5",
		TailscaleJoined: true,
		SSH: &sshidentity.Connection{
			Name:          "myvm",
			PublicIP:      "10.0.0.5",
			PreferredHost: ln.Addr().String(),
			UseIAP:        true,
		},
	}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}

	// Point IAP URL at a closed port to prove proxy used the Tailscale overlay.
	t.Setenv("GCLOUD_IAP_URL", "ws://127.0.0.1:1/v4/connect")
	t.Setenv("GCLOUD_ACCESS_TOKEN", "mock-token")

	var out bytes.Buffer
	root := newRootCmd()
	root.SetArgs([]string{"proxy", "myvm"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("proxy ExecuteContext err = %v", err)
	}
	if !strings.Contains(out.String(), "SSH-2.0-TailscaleOverlay") {
		t.Fatalf("output = %q, want SSH-2.0-TailscaleOverlay", out.String())
	}
}

func TestProxyCommand_FallsBackToIAPWhenTailscaleUnreachable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	rec := state.Record{
		Name:            "myvm",
		Provider:        "gcloud",
		Project:         "test-project",
		Region:          "us-central1-a",
		IP:              "10.0.0.5",
		TailscaleJoined: true,
		SSH: &sshidentity.Connection{
			Name:          "myvm",
			PublicIP:      "10.0.0.5",
			PreferredHost: closedAddr,
			UseIAP:        true,
		},
	}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}

	srv := newMockProxyIAPServer(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v4/connect"
	t.Setenv("GCLOUD_IAP_URL", wsURL)
	t.Setenv("GCLOUD_ACCESS_TOKEN", "mock-token")

	input := "SSH-2.0-OpenSSH_IAPFallback\r\n"
	var out bytes.Buffer
	root := newRootCmd()
	root.SetArgs([]string{"proxy", "myvm"})
	root.SetIn(strings.NewReader(input))
	root.SetOut(&out)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = root.ExecuteContext(ctx)

	if !strings.Contains(out.String(), "SSH-2.0-OpenSSH_IAPFallback") {
		t.Fatalf("output = %q, want %q", out.String(), input)
	}
}

func TestProxyCommand_FallsBackToPublicIPWhenNotUseIAP(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "SSH-2.0-PublicIPFallback\r\n")
	}()

	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedOverlay := closedLn.Addr().String()
	_ = closedLn.Close()

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	rec := state.Record{
		Name:            "pubvm",
		Provider:        "digitalocean",
		IP:              ln.Addr().String(),
		TailscaleJoined: true,
		SSH: &sshidentity.Connection{
			Name:          "pubvm",
			PublicIP:      ln.Addr().String(),
			PreferredHost: closedOverlay,
			UseIAP:        false,
		},
	}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newRootCmd()
	root.SetArgs([]string{"proxy", "pubvm"})
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("proxy ExecuteContext err = %v", err)
	}
	if !strings.Contains(out.String(), "SSH-2.0-PublicIPFallback") {
		t.Fatalf("output = %q, want SSH-2.0-PublicIPFallback", out.String())
	}
}
