package reconcile

import (
	"errors"
	"testing"
	"time"
)

type fakePinger struct {
	answer chan struct{} // closed to let the ping be answered
	closed chan struct{}
}

func (p *fakePinger) SendRequest(string, bool, []byte) (bool, []byte, error) {
	select {
	case <-p.answer:
		return false, nil, nil // a refusal still proves the peer is there
	case <-p.closed:
		return false, nil, errors.New("closed")
	}
}

func (p *fakePinger) Close() error { close(p.closed); return nil }

func TestKeepAlive_ClosesAConnectionThatStopsAnswering(t *testing.T) {
	p := &fakePinger{answer: make(chan struct{}), closed: make(chan struct{})}
	go keepAlive(p, 5*time.Millisecond, 20*time.Millisecond)

	select {
	case <-p.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("a connection that never answered was not closed, so callers wait out the OS timeout")
	}
}

func TestKeepAlive_LeavesAnAnsweringConnectionOpen(t *testing.T) {
	p := &fakePinger{answer: make(chan struct{}), closed: make(chan struct{})}
	close(p.answer)
	go keepAlive(p, 5*time.Millisecond, 20*time.Millisecond)

	select {
	case <-p.closed:
		t.Fatal("closed a connection that was answering")
	case <-time.After(100 * time.Millisecond):
	}
}
