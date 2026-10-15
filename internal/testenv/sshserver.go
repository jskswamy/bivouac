package testenv

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

// SSHServer accepts only a generated fixture key and echoes exec commands.
// It provides real authentication without executing remote shell input.
func SSHServer(t *testing.T, key sshkeys.Key) string {
	t.Helper()
	return selectedSSHServer(t, key, func(command string, channel ssh.Channel) error { _, err := channel.Write([]byte(command)); return err })
}

// StreamSSHServer streams the subprocess chosen by the fixture handler. Real
// Git and Herdr need bidirectional protocols, not canned command responses.
func StreamSSHServer(t *testing.T, key sshkeys.Key, run func(string) *exec.Cmd) string {
	t.Helper()
	return selectedSSHServer(t, key, func(command string, channel ssh.Channel) error {
		cmd := run(command)
		if cmd == nil {
			return fmt.Errorf("unsupported fixture command")
		}
		cmd.Stdin = channel
		cmd.Stdout = channel
		cmd.Stderr = channel.Stderr()
		return cmd.Run()
	})
}

func selectedSSHServer(t *testing.T, key sshkeys.Key, run func(string, ssh.Channel) error) string {
	t.Helper()
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, offered ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(public.Marshal(), offered.Marshal()) {
			return nil, fmt.Errorf("unselected identity")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "fixture")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer func() { _ = channel.Close() }()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var input struct{ Command string }
							if err := ssh.Unmarshal(request.Payload, &input); err != nil {
								return
							}
							_ = request.Reply(true, nil)
							status := uint32(0)
							if err := run(input.Command, channel); err != nil {
								status = 1
							}
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	return listener.Addr().String()
}
