# Provider Layer: Provider Interface + DigitalOcean Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `Provider` interface and a real, working DigitalOcean implementation on `godo` — VM lifecycle (`Create`/`Destroy`/`Get`/`List`) plus region/size/image discovery — tested entirely against `httptest` fakes, with zero CLI wiring.

**Architecture:** `internal/provider` holds the provider-agnostic `Provider` interface, `InstanceSpec`/`VM` value types, `ErrNotFound`, and a context-carried progress hook. `internal/provider/digitalocean` is the DO implementation on `github.com/digitalocean/godo`: `digitalocean.go` covers VM lifecycle (`Create` blocks internally, polling until the droplet is active with an IP, respecting the caller's `ctx` deadline), `discovery.go` covers `ListRegions`/`ListSizes`/`ListImages`. Both share a small generic `paginate` helper for DO's page-link-based pagination.

**Tech Stack:** Go, `github.com/digitalocean/godo` (DigitalOcean's official client — `godo.NewFromToken` handles OAuth2 internally, no separate `oauth2` import needed), Go standard library otherwise.

## Global Constraints

- Module path: `github.com/jskswamy/cloudlab`
- `Provider` interface (already locked by ADR-0008, do not change its shape):
  ```go
  type Provider interface {
      Create(ctx context.Context, spec InstanceSpec) (VM, error)
      Destroy(ctx context.Context, id string) error
      Get(ctx context.Context, id string) (VM, error)
      List(ctx context.Context) ([]VM, error)
  }
  ```
- `InstanceSpec{Name, Region, Size, Image, UserData string; SSHKeys []string}`, `VM{ID, Name, IP, Region, Size, Status string}`, `ErrNotFound = errors.New("vm not found")` — all in `internal/provider`.
- `digitalocean.New(token string) *Provider` does no network call and no validation. It does NOT read `DIGITALOCEAN_TOKEN` itself — sourcing the env var is a later phase's job.
- Testing: `httptest`-server fakes only, mirroring `godo`'s own `mux`/`httptest.Server`/`client.BaseURL` test pattern. No real DigitalOcean API calls anywhere in this plan's tests.
- `Create()` blocks until the droplet is `active` with a public IPv4 assigned, polling on an unexported `pollInterval` field (defaults to 5s in `New()`, directly overridable in same-package tests). No hardcoded timeout — `Create()` respects the caller's `ctx` deadline via `context.WithTimeout` at the call site.
- `Create()` timeout/cancellation errors must name the droplet's ID explicitly (the droplet exists and costs money — the ID must never be silently lost).
- Progress reporting is UI-agnostic and context-carried: `provider.WithProgress(ctx, fn)` / `provider.ReportProgress(ctx, status)`, no-op if unset. No terminal-UI library dependency anywhere in `internal/provider` or `internal/provider/digitalocean`.
- `ListImages` wraps `godo`'s `ImagesService.ListDistribution` (official base OS images), not `ImagesService.List` (which includes private snapshots/backups).
- `Size.GPU`/`Size.GPUModel` are derived from `godo.Size.GPUInfo` being non-nil — no separate GPU-specific field on `InstanceSpec`, since DO's create API treats GPU and CPU droplets identically (same endpoint, same request shape, just a different `size` slug).

---

### Task 1: `provider` package — interface, types, progress hook

**Files:**
- Create: `internal/provider/provider.go`
- Test: `internal/provider/provider_test.go`

**Interfaces:**
- Produces: `provider.Provider` interface; `provider.InstanceSpec`; `provider.VM`; `provider.ErrNotFound`; `provider.ProgressFunc`; `provider.WithProgress(ctx context.Context, fn ProgressFunc) context.Context`; `provider.ReportProgress(ctx context.Context, status string)`

This task has no dependency on `godo` at all — pure Go standard library.

- [ ] **Step 1: Write the failing tests**

Create `internal/provider/provider_test.go`:

```go
package provider

import (
	"context"
	"testing"
)

func TestReportProgress_CallsAttachedFunc(t *testing.T) {
	var got []string
	ctx := WithProgress(context.Background(), func(status string) {
		got = append(got, status)
	})

	ReportProgress(ctx, "first")
	ReportProgress(ctx, "second")

	want := []string{"first", "second"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReportProgress_NoopWithoutProgressFunc(t *testing.T) {
	// Must not panic when no ProgressFunc was attached via WithProgress.
	ReportProgress(context.Background(), "ignored")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/... -v`
Expected: FAIL — compile error, `undefined: WithProgress` (package `provider` has no non-test files yet).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/provider/provider.go`:

```go
// Package provider defines the VM-lifecycle abstraction every cloud
// provider implementation satisfies, plus the value types and
// cross-cutting helpers (progress reporting) shared across them.
package provider

import (
	"context"
	"errors"
)

// Provider creates, destroys, and inspects VMs. Only DigitalOcean is
// implemented; provider-specific concepts (droplet size, region, image)
// stay as direct InstanceSpec fields rather than a forced cross-provider
// abstraction.
type Provider interface {
	Create(ctx context.Context, spec InstanceSpec) (VM, error)
	Destroy(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (VM, error)
	List(ctx context.Context) ([]VM, error)
}

// InstanceSpec describes the VM to create.
type InstanceSpec struct {
	Name     string
	Region   string   // e.g. "nyc3"
	Size     string   // e.g. "s-1vcpu-1gb"
	Image    string   // e.g. "ubuntu-22-04-x64"
	SSHKeys  []string // provider SSH key IDs/fingerprints
	UserData string   // cloud-init script, opaque to this package
}

// VM is a created instance's current state.
type VM struct {
	ID     string
	Name   string
	IP     string
	Region string
	Size   string
	Status string
}

// ErrNotFound is returned by Get/Destroy when the VM no longer exists.
var ErrNotFound = errors.New("vm not found")

// ProgressFunc receives human-readable status updates during long-running
// operations (currently: Create's wait for the VM to become ready).
type ProgressFunc func(status string)

type progressKey struct{}

// WithProgress attaches fn to ctx. Provider implementations call
// ReportProgress with the resulting context to report status without
// depending on any UI library.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportProgress calls the ProgressFunc attached to ctx via WithProgress,
// if any. It is a no-op if none was set.
func ReportProgress(ctx context.Context, status string) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		fn(status)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/... -v`
Expected: PASS (both tests)

- [ ] **Step 5: Commit**

```bash
git add internal/provider/provider.go internal/provider/provider_test.go
git commit -m "Add provider interface, types, and progress hook"
```

---

### Task 2: DigitalOcean scaffold and `Get()`

**Files:**
- Create: `internal/provider/digitalocean/digitalocean.go`
- Test: `internal/provider/digitalocean/digitalocean_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `provider.VM`, `provider.ErrNotFound` (Task 1)
- Produces: `digitalocean.Provider` struct; `digitalocean.New(token string) *Provider`; unexported `parseID(id string) (int, error)`; unexported `isNotFound(err error) bool`; unexported `toVM(d *godo.Droplet) provider.VM`; `(*Provider).Get(ctx context.Context, id string) (provider.VM, error)`

This task also establishes the test harness (`newTestProvider`) that every later task in this plan reuses.

- [ ] **Step 1: Add the godo dependency**

Run: `go get github.com/digitalocean/godo@latest`
Expected: `go.mod`/`go.sum` updated with `github.com/digitalocean/godo` and its transitive deps (`golang.org/x/oauth2`, etc.).

- [ ] **Step 2: Write the failing tests**

Create `internal/provider/digitalocean/digitalocean_test.go`:

```go
package digitalocean

import (
	"context"
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
```

Add `"errors"` to the imports.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/provider/digitalocean/... -v`
Expected: FAIL — compile error, `undefined: Provider` (package `digitalocean` has no non-test files yet).

- [ ] **Step 4: Write the minimal implementation**

Create `internal/provider/digitalocean/digitalocean.go`:

```go
// Package digitalocean implements provider.Provider on top of
// DigitalOcean's godo client.
package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/cloudlab/internal/provider"
)

// Provider implements provider.Provider against the DigitalOcean API.
type Provider struct {
	client       *godo.Client
	pollInterval time.Duration
}

// New builds a Provider authenticated with token. It makes no network
// call and does not validate the token — auth failures surface on the
// first real API call.
func New(token string) *Provider {
	return &Provider{
		client:       godo.NewFromToken(token),
		pollInterval: 5 * time.Second,
	}
}

// Get returns the current state of the droplet identified by id.
func (p *Provider) Get(ctx context.Context, id string) (provider.VM, error) {
	n, err := parseID(id)
	if err != nil {
		return provider.VM{}, err
	}
	d, _, err := p.client.Droplets.Get(ctx, n)
	if err != nil {
		if isNotFound(err) {
			return provider.VM{}, fmt.Errorf("getting droplet %s: %w", id, provider.ErrNotFound)
		}
		return provider.VM{}, fmt.Errorf("getting droplet %s: %w", id, err)
	}
	return toVM(d), nil
}

// parseID converts a VM ID string (a DigitalOcean droplet ID) to the int
// godo's Droplets service expects.
func parseID(id string) (int, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("invalid droplet id %q: %w", id, err)
	}
	return n, nil
}

// isNotFound reports whether err is a DigitalOcean 404 response.
func isNotFound(err error) bool {
	var errResp *godo.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		return errResp.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// toVM converts a godo.Droplet to a provider.VM. A droplet with no
// network assigned yet (still booting) or no public IPv4 is a normal,
// non-error state — PublicIPv4's error case (Networks == nil) and its
// empty-string case (no public entry yet) both just mean VM.IP is "".
func toVM(d *godo.Droplet) provider.VM {
	ip, _ := d.PublicIPv4()
	region := ""
	if d.Region != nil {
		region = d.Region.Slug
	}
	return provider.VM{
		ID:     strconv.Itoa(d.ID),
		Name:   d.Name,
		IP:     ip,
		Region: region,
		Size:   d.SizeSlug,
		Status: d.Status,
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/provider/digitalocean/... -v`
Expected: PASS (all 3 tests)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/provider/digitalocean/digitalocean.go internal/provider/digitalocean/digitalocean_test.go
git commit -m "Add DigitalOcean provider scaffold and Get"
```

---

### Task 3: `Destroy()`

**Files:**
- Modify: `internal/provider/digitalocean/digitalocean.go`
- Modify: `internal/provider/digitalocean/digitalocean_test.go`

**Interfaces:**
- Consumes: `parseID`, `isNotFound` (Task 2)
- Produces: `(*Provider).Destroy(ctx context.Context, id string) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/digitalocean/digitalocean_test.go`:

```go
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
		w.Write([]byte(`{"id":"not_found","message":"The resource you requested could not be found."}`))
	})

	err := p.Destroy(context.Background(), "99999")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("error = %v, want it to wrap provider.ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/digitalocean/... -run TestDestroy -v`
Expected: FAIL — compile error, `undefined: (*Provider).Destroy`.

- [ ] **Step 3: Write the minimal implementation**

Append to `internal/provider/digitalocean/digitalocean.go`:

```go
// Destroy deletes the droplet identified by id.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	n, err := parseID(id)
	if err != nil {
		return err
	}
	if _, err := p.client.Droplets.Delete(ctx, n); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("destroying droplet %s: %w", id, provider.ErrNotFound)
		}
		return fmt.Errorf("destroying droplet %s: %w", id, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/digitalocean/... -v`
Expected: PASS (all tests, including Task 2's)

- [ ] **Step 5: Commit**

```bash
git add internal/provider/digitalocean/digitalocean.go internal/provider/digitalocean/digitalocean_test.go
git commit -m "Add DigitalOcean provider Destroy"
```

---

### Task 4: `List()` and the pagination helper

**Files:**
- Modify: `internal/provider/digitalocean/digitalocean.go`
- Modify: `internal/provider/digitalocean/digitalocean_test.go`

**Interfaces:**
- Consumes: `toVM` (Task 2)
- Produces: `(*Provider).List(ctx context.Context) ([]provider.VM, error)`; unexported generic `paginate[T any](acc *[]T, fetch func(*godo.ListOptions) ([]T, *godo.Response, error)) error` — reused unchanged by Task 6's discovery methods

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/digitalocean/digitalocean_test.go`:

```go
func TestList_SinglePage(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"droplets":[{"id":1,"name":"one","status":"active"},{"id":2,"name":"two","status":"active"}],"links":{},"meta":{"total":2}}`))
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
			w.Write([]byte(`{"droplets":[{"id":3,"name":"three","status":"active"}],"links":{}}`))
			return
		}
		w.Write([]byte(`{"droplets":[{"id":1,"name":"one","status":"active"}],"links":{"pages":{"next":"` + r.Host + `/v2/droplets/?page=2"}}}`))
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/digitalocean/... -run TestList -v`
Expected: FAIL — compile error, `undefined: (*Provider).List`.

- [ ] **Step 3: Write the minimal implementation**

Append to `internal/provider/digitalocean/digitalocean.go`:

```go
// List returns every droplet on the account.
func (p *Provider) List(ctx context.Context) ([]provider.VM, error) {
	var droplets []godo.Droplet
	err := paginate(&droplets, func(opt *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
		return p.client.Droplets.List(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing droplets: %w", err)
	}

	vms := make([]provider.VM, 0, len(droplets))
	for i := range droplets {
		vms = append(vms, toVM(&droplets[i]))
	}
	return vms, nil
}

// paginate loops through every page of a godo list call, appending each
// page's results to acc. Shared by List and, later, the discovery
// methods (ListRegions/ListSizes/ListImages).
func paginate[T any](acc *[]T, fetch func(*godo.ListOptions) ([]T, *godo.Response, error)) error {
	opt := &godo.ListOptions{}
	for {
		page, resp, err := fetch(opt)
		if err != nil {
			return err
		}
		*acc = append(*acc, page...)

		if resp.Links == nil || resp.Links.IsLastPage() {
			return nil
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			return err
		}
		opt.Page = current + 1
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/digitalocean/... -v`
Expected: PASS (all tests, including Tasks 2-3's)

- [ ] **Step 5: Commit**

```bash
git add internal/provider/digitalocean/digitalocean.go internal/provider/digitalocean/digitalocean_test.go
git commit -m "Add DigitalOcean provider List with pagination"
```

---

### Task 5: `Create()`

**Files:**
- Modify: `internal/provider/digitalocean/digitalocean.go`
- Modify: `internal/provider/digitalocean/digitalocean_test.go`

**Interfaces:**
- Consumes: `provider.InstanceSpec`, `provider.WithProgress`, `provider.ReportProgress` (Task 1); `toVM` (Task 2)
- Produces: `(*Provider).Create(ctx context.Context, spec provider.InstanceSpec) (provider.VM, error)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/provider/digitalocean/digitalocean_test.go`:

```go
func TestCreate_Success(t *testing.T) {
	p, mux := newTestProvider(t)

	getCalls := 0
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"new"}}`))
	})
	mux.HandleFunc("/v2/droplets/42", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		if getCalls < 3 {
			// Not ready yet: active status but no network/IP assigned.
			w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"active"}}`))
			return
		}
		w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`))
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
		w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"new"}}`))
	})
	mux.HandleFunc("/v2/droplets/42", func(w http.ResponseWriter, r *http.Request) {
		// Never becomes ready.
		w.Write([]byte(`{"droplet":{"id":42,"name":"myrepo","status":"new"}}`))
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
		w.Write([]byte(`{"droplet":{"id":1,"name":"myrepo","status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`))
	})
	mux.HandleFunc("/v2/droplets/1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"droplet":{"id":1,"name":"myrepo","status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`))
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
```

Add `"encoding/json"` and `"strings"` to the imports (`"context"` is
already imported from Task 2).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/digitalocean/... -run TestCreate -v`
Expected: FAIL — compile error, `undefined: (*Provider).Create`.

- [ ] **Step 3: Write the minimal implementation**

Append to `internal/provider/digitalocean/digitalocean.go`:

```go
// Create creates a droplet from spec and blocks until it is active with
// a public IPv4 assigned, or ctx is done.
func (p *Provider) Create(ctx context.Context, spec provider.InstanceSpec) (provider.VM, error) {
	req := &godo.DropletCreateRequest{
		Name:     spec.Name,
		Region:   spec.Region,
		Size:     spec.Size,
		Image:    godo.DropletCreateImage{Slug: spec.Image},
		SSHKeys:  sshKeys(spec.SSHKeys),
		UserData: spec.UserData,
	}

	d, _, err := p.client.Droplets.Create(ctx, req)
	if err != nil {
		return provider.VM{}, fmt.Errorf("creating droplet %q: %w", spec.Name, err)
	}
	id := d.ID

	provider.ReportProgress(ctx, "droplet created, waiting for network...")

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		got, _, err := p.client.Droplets.Get(ctx, id)
		if err != nil {
			return provider.VM{}, fmt.Errorf("waiting for droplet %d: %w", id, err)
		}
		if vm := toVM(got); vm.Status == "active" && vm.IP != "" {
			provider.ReportProgress(ctx, "active")
			return vm, nil
		}

		select {
		case <-ctx.Done():
			return provider.VM{}, fmt.Errorf("timed out waiting for droplet %d to become active: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// sshKeys converts SSH key strings to godo's request shape: a numeric
// string is treated as a key ID, anything else as a fingerprint.
func sshKeys(ids []string) []godo.DropletCreateSSHKey {
	keys := make([]godo.DropletCreateSSHKey, 0, len(ids))
	for _, id := range ids {
		if n, err := strconv.Atoi(id); err == nil {
			keys = append(keys, godo.DropletCreateSSHKey{ID: n})
			continue
		}
		keys = append(keys, godo.DropletCreateSSHKey{Fingerprint: id})
	}
	return keys
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/digitalocean/... -v`
Expected: PASS (all tests, including Tasks 2-4's)

- [ ] **Step 5: Commit**

```bash
git add internal/provider/digitalocean/digitalocean.go internal/provider/digitalocean/digitalocean_test.go
git commit -m "Add DigitalOcean provider Create"
```

---

### Task 6: Region/size/image discovery

**Files:**
- Create: `internal/provider/digitalocean/discovery.go`
- Test: `internal/provider/digitalocean/discovery_test.go`

**Interfaces:**
- Consumes: `paginate` (Task 4)
- Produces: `digitalocean.Region`, `digitalocean.Size`, `digitalocean.Image` types; `(*Provider).ListRegions(ctx context.Context) ([]Region, error)`; `(*Provider).ListSizes(ctx context.Context) ([]Size, error)`; `(*Provider).ListImages(ctx context.Context) ([]Image, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/provider/digitalocean/discovery_test.go`:

```go
package digitalocean

import (
	"context"
	"net/http"
	"testing"
)

func TestListRegions(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/regions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"regions":[{"slug":"nyc3","name":"New York 3","sizes":["s-1vcpu-1gb"],"available":true}],"links":{}}`))
	})

	regions, err := p.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions() error = %v", err)
	}
	if len(regions) != 1 {
		t.Fatalf("ListRegions() returned %d regions, want 1", len(regions))
	}
	got := regions[0]
	if got.Slug != "nyc3" || got.Name != "New York 3" || !got.Available {
		t.Errorf("ListRegions()[0] = %+v, want slug=nyc3 name=\"New York 3\" available=true", got)
	}
	if len(got.Sizes) != 1 || got.Sizes[0] != "s-1vcpu-1gb" {
		t.Errorf("ListRegions()[0].Sizes = %v, want [s-1vcpu-1gb]", got.Sizes)
	}
}

func TestListSizes_MarksGPUSizes(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sizes":[
			{"slug":"s-1vcpu-1gb","memory":1024,"vcpus":1,"disk":25,"price_monthly":6,"regions":["nyc3"],"available":true,"description":"Basic"},
			{"slug":"gpu-h100x1-80gb","memory":245760,"vcpus":20,"disk":720,"price_monthly":2699.2,"regions":["nyc2"],"available":true,"description":"GPU","gpu_info":{"count":1,"model":"h100"}}
		],"links":{}}`))
	})

	sizes, err := p.ListSizes(context.Background())
	if err != nil {
		t.Fatalf("ListSizes() error = %v", err)
	}
	if len(sizes) != 2 {
		t.Fatalf("ListSizes() returned %d sizes, want 2", len(sizes))
	}

	cpu, gpu := sizes[0], sizes[1]
	if cpu.GPU {
		t.Errorf("cpu size %q: GPU = true, want false", cpu.Slug)
	}
	if !gpu.GPU || gpu.GPUModel != "h100" {
		t.Errorf("gpu size %q: GPU=%v GPUModel=%q, want GPU=true GPUModel=h100", gpu.Slug, gpu.GPU, gpu.GPUModel)
	}
	if gpu.VCPUs != 20 || gpu.MemoryMB != 245760 || gpu.DiskGB != 720 {
		t.Errorf("gpu size = %+v, want VCPUs=20 MemoryMB=245760 DiskGB=720", gpu)
	}
}

func TestListImages_UsesDistributionEndpoint(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "distribution" {
			t.Errorf("query type = %q, want distribution", r.URL.Query().Get("type"))
		}
		w.Write([]byte(`{"images":[{"slug":"ubuntu-22-04-x64","name":"Ubuntu 22.04","distribution":"Ubuntu","public":true,"regions":["nyc3"]}],"links":{}}`))
	})

	images, err := p.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages() error = %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("ListImages() returned %d images, want 1", len(images))
	}
	got := images[0]
	if got.Slug != "ubuntu-22-04-x64" || got.Distribution != "Ubuntu" || !got.Public {
		t.Errorf("ListImages()[0] = %+v, want slug=ubuntu-22-04-x64 distribution=Ubuntu public=true", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provider/digitalocean/... -run "TestListRegions|TestListSizes|TestListImages" -v`
Expected: FAIL — compile error, `undefined: (*Provider).ListRegions`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/provider/digitalocean/discovery.go`:

```go
package digitalocean

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

// Region, Size, and Image are DigitalOcean-specific discovery results —
// deliberately not part of the generic provider package, since region/
// size/image vocabulary doesn't generalize across cloud providers.
type Region struct {
	Slug, Name string
	Sizes      []string
	Available  bool
}

type Size struct {
	Slug, Description       string
	VCPUs, MemoryMB, DiskGB int
	PriceMonthly            float64
	Regions                 []string
	Available, GPU          bool
	GPUModel                string
}

type Image struct {
	Slug, Name, Distribution string
	Public                   bool
	Regions                  []string
}

// ListRegions returns every DigitalOcean region, including which size
// slugs are available in each.
func (p *Provider) ListRegions(ctx context.Context) ([]Region, error) {
	var raw []godo.Region
	err := paginate(&raw, func(opt *godo.ListOptions) ([]godo.Region, *godo.Response, error) {
		return p.client.Regions.List(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing regions: %w", err)
	}

	regions := make([]Region, 0, len(raw))
	for _, r := range raw {
		regions = append(regions, Region{
			Slug:      r.Slug,
			Name:      r.Name,
			Sizes:     r.Sizes,
			Available: r.Available,
		})
	}
	return regions, nil
}

// ListSizes returns every DigitalOcean droplet size, including GPU
// sizes (GPU is true and GPUModel is set when godo reports GPUInfo).
func (p *Provider) ListSizes(ctx context.Context) ([]Size, error) {
	var raw []godo.Size
	err := paginate(&raw, func(opt *godo.ListOptions) ([]godo.Size, *godo.Response, error) {
		return p.client.Sizes.List(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing sizes: %w", err)
	}

	sizes := make([]Size, 0, len(raw))
	for _, s := range raw {
		size := Size{
			Slug:         s.Slug,
			Description:  s.Description,
			VCPUs:        s.Vcpus,
			MemoryMB:     s.Memory,
			DiskGB:       s.Disk,
			PriceMonthly: s.PriceMonthly,
			Regions:      s.Regions,
			Available:    s.Available,
		}
		if s.GPUInfo != nil {
			size.GPU = true
			size.GPUModel = s.GPUInfo.Model
		}
		sizes = append(sizes, size)
	}
	return sizes, nil
}

// ListImages returns DigitalOcean's official base distribution images
// (not private snapshots/backups).
func (p *Provider) ListImages(ctx context.Context) ([]Image, error) {
	var raw []godo.Image
	err := paginate(&raw, func(opt *godo.ListOptions) ([]godo.Image, *godo.Response, error) {
		return p.client.Images.ListDistribution(ctx, opt)
	})
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	images := make([]Image, 0, len(raw))
	for _, img := range raw {
		images = append(images, Image{
			Slug:         img.Slug,
			Name:         img.Name,
			Distribution: img.Distribution,
			Public:       img.Public,
			Regions:      img.Regions,
		})
	}
	return images, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provider/digitalocean/... -v`
Expected: PASS (all tests, including Tasks 2-5's)

- [ ] **Step 5: Run the full test suite and build**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: builds cleanly, `go vet` and `gofmt -l` report nothing, every test across `cmd`, `internal/identity`, `internal/state`, `internal/provider`, `internal/provider/digitalocean` passes.

- [ ] **Step 6: Add a permanent compile-time interface check**

Add this line to `internal/provider/digitalocean/digitalocean.go` right after the `Provider` struct definition. It stays permanently (a standard Go idiom, not a temporary check to remove later) — it gives a compile-time guarantee that `*Provider` actually implements `provider.Provider`, catching a signature typo immediately instead of only at first real use:

```go
var _ provider.Provider = (*Provider)(nil)
```

Run: `go build ./...`
Expected: builds cleanly — if this line fails to compile, `*Provider` doesn't fully satisfy `provider.Provider`; fix the mismatched method before continuing.

- [ ] **Step 7: Commit**

```bash
git add internal/provider/digitalocean/discovery.go internal/provider/digitalocean/discovery_test.go internal/provider/digitalocean/digitalocean.go
git commit -m "Add DigitalOcean region, size, and image discovery"
```
