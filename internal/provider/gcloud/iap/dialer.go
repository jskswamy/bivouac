package iap

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	Origin              = "bot:iap-tunneler"
	Subprotocol         = "relay.tunnel.cloudproxy.app"
	TagConnectSuccess   = 1
	TagReconnectSuccess = 2
	TagData             = 4
	TagAck              = 7

	DefaultEndpoint = "wss://tunnel.cloudproxy.app/v4/connect"
	MaxPayloadSize  = 16384
)

// Conn implements net.Conn over an IAP WebSocket tunnel.
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	rmu           sync.Mutex
	rbuf          bytes.Buffer
	bytesReceived uint64
	bytesAcked    uint64

	wmu     sync.Mutex
	closeMu sync.Mutex
	closed  bool
}

// Dial connects to a GCE instance over Cloud IAP TCP forwarding.
func Dial(ctx context.Context, project, zone, instance string, port int, token string) (net.Conn, error) {
	u, err := url.Parse(DefaultEndpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("project", project)
	q.Set("zone", zone)
	q.Set("instance", instance)
	q.Set("port", fmt.Sprintf("%d", port))
	q.Set("interface", "nic0")
	u.RawQuery = q.Encode()

	return DialWithURL(ctx, u.String(), token)
}

// DialWithURL connects to the given WebSocket URL using an access token.
func DialWithURL(ctx context.Context, wsURL, token string) (*Conn, error) {
	headers := make(http.Header)
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	headers.Set("Origin", Origin)

	opts := &websocket.DialOptions{
		HTTPHeader:   headers,
		Subprotocols: []string{Subprotocol},
	}

	connCtx, cancel := context.WithCancel(ctx)
	ws, _, err := websocket.Dial(connCtx, wsURL, opts)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dialing iap websocket: %w", err)
	}

	return &Conn{
		ws:     ws,
		ctx:    connCtx,
		cancel: cancel,
	}, nil
}

func (c *Conn) isClosed() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.closed
}

func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	for c.rbuf.Len() == 0 {
		if c.isClosed() {
			return 0, io.EOF
		}

		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return 0, io.EOF
			}
			return 0, err
		}
		if typ != websocket.MessageBinary || len(data) < 6 {
			continue
		}

		tag := binary.BigEndian.Uint16(data[0:2])
		switch tag {
		case TagConnectSuccess, TagReconnectSuccess, TagAck:
			// Handshake or control frames
			continue
		case TagData:
			length := binary.BigEndian.Uint32(data[2:6])
			var payload []byte
			if int(6+length) <= len(data) {
				payload = data[6 : 6+length]
			} else {
				payload = data[6:]
			}
			c.rbuf.Write(payload)
			c.bytesReceived += uint64(len(payload))
			if c.bytesReceived-c.bytesAcked > 2*MaxPayloadSize {
				_ = c.sendAck()
			}
		default:
			// Unknown tag, ignore per spec
			continue
		}
	}

	return c.rbuf.Read(p)
}

func (c *Conn) sendAck() error {
	buf := make([]byte, 10)
	binary.BigEndian.PutUint16(buf[0:2], TagAck)
	binary.BigEndian.PutUint64(buf[2:10], c.bytesReceived)

	c.wmu.Lock()
	defer c.wmu.Unlock()

	if c.isClosed() {
		return io.ErrClosedPipe
	}
	if err := c.ws.Write(c.ctx, websocket.MessageBinary, buf); err != nil {
		return err
	}
	c.bytesAcked = c.bytesReceived
	return nil
}

func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	if c.isClosed() {
		return 0, io.ErrClosedPipe
	}

	totalWritten := 0
	for len(p) > 0 {
		chunkSize := len(p)
		if chunkSize > MaxPayloadSize {
			chunkSize = MaxPayloadSize
		}
		chunk := p[:chunkSize]
		p = p[chunkSize:]

		// Frame as TagData: uint16(4) + uint32(len) + payload
		buf := make([]byte, 6+chunkSize)
		binary.BigEndian.PutUint16(buf[0:2], TagData)
		// #nosec G115 -- chunkSize is bounded by MaxPayloadSize
		binary.BigEndian.PutUint32(buf[2:6], uint32(chunkSize))
		copy(buf[6:], chunk)

		if err := c.ws.Write(c.ctx, websocket.MessageBinary, buf); err != nil {
			return totalWritten, err
		}
		totalWritten += chunkSize
	}
	return totalWritten, nil
}

func (c *Conn) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	c.cancel()
	c.closeMu.Unlock()

	return c.ws.Close(websocket.StatusNormalClosure, "closed")
}

func (c *Conn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (c *Conn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
}

func (c *Conn) SetDeadline(t time.Time) error      { return nil }
func (c *Conn) SetReadDeadline(t time.Time) error  { return nil }
func (c *Conn) SetWriteDeadline(t time.Time) error { return nil }

// DialTarget resolves credentials and establishes an IAP tunnel connection to the
// specified instance. If GCLOUD_IAP_URL is set in the environment, it overrides
// the destination endpoint (primarily for testing with gcpfake).
func DialTarget(ctx context.Context, project, zone, instance string, port int) (net.Conn, error) {
	token, err := ResolveAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving gcloud access token: %w", err)
	}
	if raw := os.Getenv("GCLOUD_IAP_URL"); raw != "" {
		return DialWithURL(ctx, raw, token)
	}
	if port <= 0 {
		port = 22
	}
	return Dial(ctx, project, zone, instance, port, token)
}
