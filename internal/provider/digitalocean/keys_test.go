package digitalocean

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
)

// The wizard reaches this capability by type assertion, so the
// assertion has to hold.
func TestProvider_IsAKeyRegistry(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.KeyRegistry); !ok {
		t.Fatal("*Provider does not implement provider.KeyRegistry")
	}
}

func TestListKeys(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/account/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ssh_keys":[
			{"id":1,"name":"laptop","fingerprint":"3b:16:bf:aa","public_key":"ssh-ed25519 AAAA laptop"},
			{"id":2,"name":"desktop","fingerprint":"a1:9c:00:11","public_key":"ssh-ed25519 BBBB desktop"}
		]}`))
	})

	keys, err := p.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("ListKeys() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	if keys[0].ID != "1" || keys[0].Name != "laptop" || keys[0].Fingerprint != "3b:16:bf:aa" {
		t.Errorf("keys[0] = %+v", keys[0])
	}
	if keys[1].Fingerprint != "a1:9c:00:11" {
		t.Errorf("keys[1] = %+v", keys[1])
	}
}

// A token scoped without the key permission is the recommended setup,
// not a mistake. The caller has to be able to tell that apart from a
// real failure so it can drop the tier rather than fail the run.
func TestListKeys_ForbiddenIsDistinguishable(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/account/keys", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"id":"forbidden","message":"token missing required scope"}`))
	})

	_, err := p.ListKeys(context.Background())
	if err == nil {
		t.Fatal("ListKeys() error = nil, want a forbidden error")
	}
	if !provider.IsForbidden(err) {
		t.Errorf("IsForbidden(%v) = false, want true", err)
	}
}

func TestCreateKey(t *testing.T) {
	p, mux := newTestProvider(t)
	var body string
	mux.HandleFunc("/v2/account/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		body = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ssh_key":{"id":7,"name":"old-desktop","fingerprint":"c3:d4","public_key":"ssh-ed25519 CCCC old"}}`))
	})

	key, err := p.CreateKey(context.Background(), "old-desktop", "ssh-ed25519 CCCC old")
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if key.Fingerprint != "c3:d4" || key.ID != "7" {
		t.Errorf("key = %+v", key)
	}
	if !strings.Contains(body, `"name":"old-desktop"`) || !strings.Contains(body, `"public_key":"ssh-ed25519 CCCC old"`) {
		t.Errorf("request body = %s, want the name and public key", body)
	}
}

func TestCreateKey_ForbiddenIsDistinguishable(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/account/keys", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"id":"forbidden","message":"token cannot create keys"}`))
	})

	_, err := p.CreateKey(context.Background(), "n", "ssh-ed25519 AAAA n")
	if err == nil {
		t.Fatal("CreateKey() error = nil, want a forbidden error")
	}
	if !provider.IsForbidden(err) {
		t.Errorf("IsForbidden(%v) = false, want true", err)
	}
}
