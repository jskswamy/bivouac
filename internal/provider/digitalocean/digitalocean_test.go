package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/cloudlab/internal/provider"
)

// newTestProvider spins up an httptest.Server backed by mux, points a
// godo client at it (mirroring godo's own test suite's setup()), and
// returns a Provider built directly (bypassing New()) so pollInterval
// can be set to something tests don't have to wait real time for.
func newTestProvider(t *testing.T) (*Provider, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := godo.NewFromToken("test-token")
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client.BaseURL = baseURL

	return &Provider{client: client, pollInterval: time.Millisecond}, mux
}

func TestGet_Found(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets/12345", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"droplet":{"id":12345,"name":"myrepo","status":"active"}}`))
	})

	vm, err := p.Get(context.Background(), "12345")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if vm.ID != "12345" {
		t.Errorf("vm.ID = %q, want %q", vm.ID, "12345")
	}
	if vm.Name != "myrepo" {
		t.Errorf("vm.Name = %q, want %q", vm.Name, "myrepo")
	}
	if vm.Status != "active" {
		t.Errorf("vm.Status = %q, want %q", vm.Status, "active")
	}
}

func TestGet_NotFound(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets/99999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"id":"not_found","message":"The resource you requested could not be found."}`))
	})

	_, err := p.Get(context.Background(), "99999")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want it to wrap provider.ErrNotFound", err)
	}
}

func TestGet_InvalidID(t *testing.T) {
	p, _ := newTestProvider(t)

	_, err := p.Get(context.Background(), "not-a-number")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
