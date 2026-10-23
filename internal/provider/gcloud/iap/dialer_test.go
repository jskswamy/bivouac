package iap_test

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/iap"
)

func newMockIAPServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v4/connect") {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			Subprotocols:       []string{iap.Subprotocol},
		})
		if err != nil {
			t.Logf("websocket accept: %v", err)
			return
		}
		defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()

		// Send CONNECT_SUCCESS (tag 1)
		sid := []byte("test-session-id")
		connectSuccess := make([]byte, 6+len(sid))
		binary.BigEndian.PutUint16(connectSuccess[0:2], iap.TagConnectSuccess)
		binary.BigEndian.PutUint32(connectSuccess[2:6], uint32(len(sid)))
		copy(connectSuccess[6:], sid)
		if err := c.Write(r.Context(), websocket.MessageBinary, connectSuccess); err != nil {
			return
		}

		// Echo loop for DATA packets
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary || len(data) < 6 {
				continue
			}
			tag := binary.BigEndian.Uint16(data[0:2])
			if tag == iap.TagData {
				// Echo the same data back
				if err := c.Write(r.Context(), websocket.MessageBinary, data); err != nil {
					return
				}
			}
		}
	}))
	return s
}

func TestDial_EchoOverIAP(t *testing.T) {
	srv := newMockIAPServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v4/connect"
	conn, err := iap.DialWithURL(ctx, wsURL, "test-token")
	if err != nil {
		t.Fatalf("DialWithURL() err = %v", err)
	}
	defer func() { _ = conn.Close() }()

	msg := []byte("hello IAP over websocket")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("conn.Write() err = %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("conn.Read() err = %v", err)
	}
	if string(buf) != string(msg) {
		t.Errorf("read %q, want %q", string(buf), string(msg))
	}
}
