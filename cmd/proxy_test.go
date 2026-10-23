package cmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/iap"
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
