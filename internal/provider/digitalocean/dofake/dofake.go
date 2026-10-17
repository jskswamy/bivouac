// Package dofake is an in-memory DigitalOcean API, served over
// httptest, for driving the real godo client and the real
// digitalocean.Provider without an account.
//
// It is stateful so that a test can follow a sequence across calls
// (build, tag, list, prune) the way per-test ServeMux fixtures cannot,
// and it marshals godo's own types inside DigitalOcean's envelopes so
// that it agrees with the client by construction. What it agrees with is
// godo's model of the API, not necessarily the live API; the provider
// package's hand-written fixtures and a paid acceptance run cover that
// difference.
//
// It is an ordinary package rather than test-only code because the
// command tests and the QEMU end-to-end harness share it.
package dofake

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- DigitalOcean's key fingerprints are MD5; nothing here is secured by it.
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean"
)

// Request is one request the fake received, recorded before any fault
// or route handled it.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   []byte
}

// Backend is what happens to a droplet outside the API's bookkeeping.
// The default one hands out documentation addresses and does nothing
// else; the end-to-end harness supplies one that boots QEMU VMs.
//
// The fake calls it without holding its lock, so a slow backend (a VM
// booting, a disk being copied) does not stall other requests.
type Backend interface {
	// Create starts the droplet and returns its public IPv4 address.
	Create(d godo.Droplet, userData string, keys []godo.Key) (ip string, err error)
	PowerOff(id int) error
	// Snapshot captures the droplet's disk as imageID and returns the
	// disk's size in GB; 0 means "the droplet's size's disk".
	Snapshot(id int, imageID int) (minDiskGB int, err error)
	Destroy(id int) error
}

// Server is the fake API. Its zero value is not usable; call New.
type Server struct {
	srv *httptest.Server
	mux *http.ServeMux

	mu       sync.Mutex
	backend  Backend
	droplets map[int]*dropletRecord
	images   map[int]*godo.Image
	// pendingReads counts how many more reads of an image report it
	// pending before it becomes available.
	pendingReads    map[int]int
	snapshotPending int
	keys            map[int]*godo.Key
	tags            map[string]bool
	sizes           []godo.Size
	actions         map[int]*godo.Action
	nextID          map[string]int
	// clock stamps created_at. It advances a second per use, so two
	// things created in one test are never tied on DigitalOcean's
	// second-resolution timestamps and "oldest first" is well defined.
	clock    time.Time
	requests []Request
	unknown  []string
	faults   []*fault
	hooks    []*hook
}

type dropletRecord struct {
	droplet  godo.Droplet
	userData string
}

type fault struct {
	match  *http.ServeMux
	status int
	times  int
}

type hook struct {
	match *http.ServeMux
	run   func(*Server)
}

// New starts a fake API and stops it when t's test ends. At that point
// every request the fake did not model is reported through t.Errorf:
// an unknown route gets a 501 rather than an empty success, and failing
// the test on it keeps a new API call from going untested.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		backend:      &defaultBackend{},
		droplets:     map[int]*dropletRecord{},
		images:       map[int]*godo.Image{},
		pendingReads: map[int]int{},
		keys:         map[int]*godo.Key{},
		tags:         map[string]bool{},
		actions:      map[int]*godo.Action{},
		// Distinct ranges per kind, so a droplet ID passed where an
		// image ID belongs finds nothing instead of the wrong thing.
		nextID: map[string]int{"droplet": 3000001, "image": 7000001, "key": 4000001, "action": 1000001},
		clock:  time.Now().UTC().Truncate(time.Second),
		mux:    http.NewServeMux(),
	}
	s.routes()
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		s.srv.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, u := range s.unknown {
			t.Errorf("dofake: request the fake does not model: %s", u)
		}
	})
	return s
}

// URL is the base URL to point a client at, without a trailing slash.
func (s *Server) URL() string { return s.srv.URL }

// Provider returns the real DigitalOcean provider pointed at this fake,
// polling every millisecond so that Create never waits real time.
//
// The provider keeps godo's default retries: a 429 or 5xx is retried up
// to four times with real backoff (Retry-After for a 429, 1 s doubling
// for a 5xx), so a failure meant to reach the caller must be injected
// at least five times, and costs that much wall time.
func (s *Server) Provider(t testing.TB) *digitalocean.Provider {
	t.Helper()
	p, err := digitalocean.New("dofake-token",
		digitalocean.WithBaseURL(s.URL()),
		digitalocean.WithPollInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("dofake: building provider: %v", err)
	}
	return p
}

// SetBackend replaces the default backend.
func (s *Server) SetBackend(b Backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = b
}

func (s *Server) allocate(kind string) int {
	id := s.nextID[kind]
	s.nextID[kind]++
	return id
}

func (s *Server) reserve(kind string, id int) {
	if id >= s.nextID[kind] {
		s.nextID[kind] = id + 1
	}
}

// AddImage seeds an image, assigning an ID when it has none. An image
// without a status is available and one without a creation time was
// created now, so a seed only states what a test cares about.
func (s *Server) AddImage(img godo.Image) godo.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	if img.ID == 0 {
		img.ID = s.allocate("image")
	}
	s.reserve("image", img.ID)
	if img.Status == "" {
		img.Status = "available"
	}
	if img.Created == "" {
		img.Created = s.now()
	}
	stored := copyImage(img)
	s.images[img.ID] = &stored
	return copyImage(stored)
}

// RemoveImage deletes an image as another actor would, outside the API.
// It reports whether the image existed.
func (s *Server) RemoveImage(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.images[id]
	delete(s.images, id)
	delete(s.pendingReads, id)
	return ok
}

// AddSize seeds a droplet size, replacing one with the same slug.
// Creates are refused for sizes that were never seeded.
func (s *Server) AddSize(size godo.Size) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sizes = slices.DeleteFunc(s.sizes, func(z godo.Size) bool { return z.Slug == size.Slug })
	s.sizes = append(s.sizes, size)
}

// AddKey seeds an SSH key, assigning an ID and fingerprint when absent.
func (s *Server) AddKey(k godo.Key) godo.Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k.ID == 0 {
		k.ID = s.allocate("key")
	}
	s.reserve("key", k.ID)
	if k.Fingerprint == "" {
		k.Fingerprint, _ = fingerprint(k.PublicKey)
	}
	stored := k
	s.keys[k.ID] = &stored
	return k
}

// Images returns every image, oldest ID first. Unlike a GET it does not
// count as a read of a pending snapshot.
func (s *Server) Images() []godo.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]godo.Image, 0, len(s.images))
	for _, id := range sortedKeys(s.images) {
		out = append(out, copyImage(*s.images[id]))
	}
	return out
}

// Droplets returns every droplet that has not been destroyed.
func (s *Server) Droplets() []godo.Droplet {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]godo.Droplet, 0, len(s.droplets))
	for _, id := range sortedKeys(s.droplets) {
		out = append(out, s.droplets[id].droplet)
	}
	return out
}

// Keys returns every SSH key on the account.
func (s *Server) Keys() []godo.Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]godo.Key, 0, len(s.keys))
	for _, id := range sortedKeys(s.keys) {
		out = append(out, *s.keys[id])
	}
	return out
}

// Requests returns every request received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	for i, r := range s.requests {
		r.Body = bytes.Clone(r.Body)
		out[i] = r
	}
	return out
}

// SetSnapshotPending makes every later snapshot read as pending for n
// reads before it is available. A read is any response that carries the
// image, a listing included, standing in for the time a real snapshot
// takes while its builder polls.
func (s *Server) SetSnapshotPending(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotPending = n
}

var errNoDroplet = errors.New("no such droplet")

// Snapshot does what the Packer plugin's snapshot step does through the
// API: it captures dropletID as a snapshot image called name, in the
// droplet's region.
func (s *Server) Snapshot(dropletID int, name string) (godo.Image, error) {
	s.mu.Lock()
	rec, ok := s.droplets[dropletID]
	if !ok {
		s.mu.Unlock()
		return godo.Image{}, fmt.Errorf("snapshot of droplet %d: %w", dropletID, errNoDroplet)
	}
	d := rec.droplet
	imageID := s.allocate("image")
	backend := s.backend
	s.mu.Unlock()

	minDisk, err := backend.Snapshot(dropletID, imageID)
	if err != nil {
		return godo.Image{}, fmt.Errorf("snapshot of droplet %d: %w", dropletID, err)
	}
	if minDisk == 0 {
		minDisk = d.Disk
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	img := godo.Image{
		ID:            imageID,
		Name:          name,
		Type:          "snapshot",
		Regions:       []string{regionSlug(&d)},
		MinDiskSize:   minDisk,
		SizeGigaBytes: 2.5,
		Created:       s.now(),
		Status:        "available",
	}
	if d.Image != nil {
		img.Distribution = d.Image.Distribution
	}
	if s.snapshotPending > 0 {
		img.Status = "pending"
		s.pendingReads[imageID] = s.snapshotPending
	}
	s.images[imageID] = &img
	if rec, ok := s.droplets[dropletID]; ok {
		rec.droplet.SnapshotIDs = append(rec.droplet.SnapshotIDs, imageID)
	}
	return copyImage(img), nil
}

// readImage returns the image as a client sees it now and advances a
// pending snapshot towards available. Callers hold s.mu.
func (s *Server) readImage(id int) godo.Image {
	stored := s.images[id]
	out := copyImage(*stored)
	if n, ok := s.pendingReads[id]; ok {
		if n <= 1 {
			delete(s.pendingReads, id)
			stored.Status = "available"
		} else {
			s.pendingReads[id] = n - 1
		}
	}
	return out
}

// Fail makes the next times requests matching method and pattern (a
// ServeMux path pattern such as "/v2/images/{id}") answer status with a
// DigitalOcean error body instead of reaching the route. A 429 carries
// Retry-After and the ratelimit headers DigitalOcean sends.
//
// An invalid pattern panics, as ServeMux does: it is a bug in the test.
func (s *Server) Fail(method, pattern string, status, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &fault{match: matcher(method, pattern), status: status, times: times})
}

// Before runs hook once, just before the next request matching method
// and pattern is handled. The hook may change the fake's state, which is
// how a test models another actor acting between a plan and its effect.
func (s *Server) Before(method, pattern string, run func(*Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, &hook{match: matcher(method, pattern), run: run})
}

func matcher(method, pattern string) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc(method+" "+pattern, func(http.ResponseWriter, *http.Request) {})
	return m
}

func matches(m *http.ServeMux, r *http.Request) bool {
	_, pattern := m.Handler(r)
	return pattern != ""
}

// serve records the request, runs any hook and fault due on it, and
// only then routes it.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
	var due []*hook
	s.hooks = slices.DeleteFunc(s.hooks, func(h *hook) bool {
		if matches(h.match, r) {
			due = append(due, h)
			return true
		}
		return false
	})
	s.mu.Unlock()

	for _, h := range due {
		h.run(s)
	}

	if status, ok := s.takeFault(r); ok {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Ratelimit-Limit", "5000")
			w.Header().Set("Ratelimit-Remaining", "0")
			w.Header().Set("Ratelimit-Reset", fmt.Sprint(time.Now().Add(time.Second).Unix()))
		}
		writeError(w, status, errorID(status), errorMessage(status)+" (injected by dofake)")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) takeFault(r *http.Request) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.faults {
		if !matches(f.match, r) {
			continue
		}
		f.times--
		if f.times <= 0 {
			s.faults = slices.Delete(s.faults, i, i+1)
		}
		return f.status, true
	}
	return 0, false
}

// errorMessage is the message DigitalOcean sends with status, so that
// code which surfaces the API's message is tested against what a user
// would actually read.
func errorMessage(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "Unable to authenticate you"
	case http.StatusForbidden:
		return "You do not have access for the attempted action."
	case http.StatusNotFound:
		return "The resource you were accessing could not be found."
	case http.StatusTooManyRequests:
		return "API Rate limit exceeded."
	default:
		return "Server was unable to give you a response."
	}
}

func errorID(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusTooManyRequests:
		return "too_many_requests"
	default:
		return "server_error"
	}
}

// defaultBackend gives each droplet the next address in 192.0.2.0/24,
// the documentation range, so nothing can mistake it for a real host.
type defaultBackend struct {
	mu   sync.Mutex
	next int
}

func (b *defaultBackend) Create(godo.Droplet, string, []godo.Key) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	if b.next > 254 {
		return "", errors.New("dofake: default backend ran out of addresses")
	}
	return fmt.Sprintf("192.0.2.%d", b.next), nil
}

func (b *defaultBackend) PowerOff(int) error             { return nil }
func (b *defaultBackend) Snapshot(int, int) (int, error) { return 0, nil }
func (b *defaultBackend) Destroy(int) error              { return nil }

// fingerprint is DigitalOcean's: the MD5 of the decoded key blob, as
// colon-separated hex.
func fingerprint(publicKey string) (string, error) {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return "", errors.New("key must be \"<type> <base64> [comment]\"")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", fmt.Errorf("key is not base64: %w", err)
	}
	sum := md5.Sum(blob) // #nosec G401 -- see the import
	hex := make([]string, len(sum))
	for i, b := range sum {
		hex[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(hex, ":"), nil
}

func copyImage(img godo.Image) godo.Image {
	img.Regions = slices.Clone(img.Regions)
	img.Tags = slices.Clone(img.Tags)
	return img
}

func regionSlug(d *godo.Droplet) string {
	if d.Region == nil {
		return ""
	}
	return d.Region.Slug
}

func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// now returns the next created_at. Callers hold s.mu.
func (s *Server) now() string {
	s.clock = s.clock.Add(time.Second)
	return s.clock.Format(time.RFC3339)
}
