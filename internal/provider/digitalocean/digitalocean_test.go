package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		_, _ = w.Write([]byte(`{"droplet":{"id":12345,"name":"myrepo","status":"active","region":{"slug":"nyc3"},"size_slug":"s-1vcpu-1gb"}}`))
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
	if vm.Region != "nyc3" {
		t.Errorf("vm.Region = %q, want %q", vm.Region, "nyc3")
	}
	if vm.Size != "s-1vcpu-1gb" {
		t.Errorf("vm.Size = %q, want %q", vm.Size, "s-1vcpu-1gb")
	}
}

func TestGet_NotFound(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets/99999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"The resource you requested could not be found."}`))
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

func TestDestroy_Found(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets/12345", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := p.Destroy(context.Background(), "12345"); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
}

func TestDestroy_NotFound(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets/99999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"The resource you requested could not be found."}`))
	})

	err := p.Destroy(context.Background(), "99999")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want it to wrap provider.ErrNotFound", err)
	}
}

func TestDestroy_NonNotFoundErrorNotMisclassified(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets/12345", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"id":"server_error","message":"Something went wrong on our end."}`))
	})

	err := p.Destroy(context.Background(), "12345")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want it NOT to wrap provider.ErrNotFound for a 500 response", err)
	}
}

func TestList_SinglePage(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"droplets":[{"id":1,"name":"one","status":"active"},{"id":2,"name":"two","status":"active"}],"links":{},"meta":{"total":2}}`))
	})

	vms, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("List() returned %d VMs, want 2", len(vms))
	}
	if vms[0].ID != "1" || vms[1].ID != "2" {
		t.Errorf("List() = %+v, want IDs 1 and 2", vms)
	}
}

func TestList_MultiplePages(t *testing.T) {
	p, mux := newTestProvider(t)
	requests := 0
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"droplets":[{"id":3,"name":"three","status":"active"}],"links":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"droplets":[{"id":1,"name":"one","status":"active"}],"links":{"pages":{"next":"` + r.Host + `/v2/droplets/?page=2"}}}`))
	})

	vms, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("handler called %d times, want 2 (one per page)", requests)
	}
	if len(vms) != 2 {
		t.Fatalf("List() returned %d VMs across 2 pages, want 2", len(vms))
	}
	if vms[0].ID != "1" || vms[1].ID != "3" {
		t.Errorf("List() = %+v, want IDs 1 and 3", vms)
	}
}

func TestCreate_Success(t *testing.T) {
	p, mux := newTestProvider(t)

	getCalls := 0
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		_, _ = w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"new"}}`))
	})
	mux.HandleFunc("/v2/droplets/42", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		if getCalls < 3 {
			// Not ready yet: active status but no network/IP assigned.
			_, _ = w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"active"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`))
	})

	var progress []string
	ctx := provider.WithProgress(context.Background(), func(status string) {
		progress = append(progress, status)
	})

	vm, err := p.Create(ctx, provider.InstanceSpec{
		Name:   "myrepo",
		Region: "nyc3",
		Size:   "s-1vcpu-1gb",
		Image:  "ubuntu-22-04-x64",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if vm.ID != "42" || vm.IP != "203.0.113.5" || vm.Status != "active" {
		t.Errorf("Create() = %+v, want ID 42, IP 203.0.113.5, status active", vm)
	}
	if getCalls < 3 {
		t.Errorf("Get was polled %d times, want the loop to actually loop (>=3)", getCalls)
	}
	if len(progress) != 2 || progress[0] != "droplet created, waiting for network..." || progress[1] != "active" {
		t.Errorf("progress = %v, want [\"droplet created, waiting for network...\", \"active\"]", progress)
	}
}

func TestCreate_TimeoutNamesDropletID(t *testing.T) {
	p, mux := newTestProvider(t)

	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		_, _ = w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"new"}}`))
	})
	mux.HandleFunc("/v2/droplets/42", func(w http.ResponseWriter, r *http.Request) {
		// Never becomes ready.
		_, _ = w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"new"}}`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := p.Create(ctx, provider.InstanceSpec{Name: "myrepo", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-22-04-x64"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("error = %q, want it to name droplet ID 42", err.Error())
	}
}

func TestCreate_SSHKeysParsedAsIDOrFingerprint(t *testing.T) {
	p, mux := newTestProvider(t)

	var gotBody map[string]interface{}
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"droplet":{"id":1,"name":"myrepo","status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`))
	})
	mux.HandleFunc("/v2/droplets/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"droplet":{"id":1,"name":"myrepo","status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`))
	})

	_, err := p.Create(context.Background(), provider.InstanceSpec{
		Name: "myrepo", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-22-04-x64",
		SSHKeys: []string{"12345", "aa:bb:cc:dd"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// DropletCreateSSHKey.MarshalJSON emits a bare scalar per key (the
	// numeric ID or the fingerprint string), not an {"id": ...} object.
	keys, ok := gotBody["ssh_keys"].([]interface{})
	if !ok || len(keys) != 2 {
		t.Fatalf("ssh_keys in request = %#v, want 2 entries", gotBody["ssh_keys"])
	}
	if id, ok := keys[0].(float64); !ok || id != 12345 {
		t.Errorf("keys[0] = %#v, want 12345", keys[0])
	}
	if fp, ok := keys[1].(string); !ok || fp != "aa:bb:cc:dd" {
		t.Errorf("keys[1] = %#v, want \"aa:bb:cc:dd\"", keys[1])
	}
}
