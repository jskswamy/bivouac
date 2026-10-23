package gcpfake

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/option"
)

// Server is an in-memory mock Google Compute Engine API server.
type Server struct {
	srv *httptest.Server
	mux *http.ServeMux

	mu        sync.Mutex
	opCounter uint64

	instances  map[string]*computepb.Instance  // key: project/zone/name
	operations map[string]*computepb.Operation // key: project/zone/name or project/global/name
	disks      map[string]*computepb.Disk      // key: project/zone/name
	images     map[string]*computepb.Image     // key: project/name
}

// NewServer starts and returns a new mock GCE server for testing.
func NewServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		mux:        http.NewServeMux(),
		instances:  make(map[string]*computepb.Instance),
		operations: make(map[string]*computepb.Operation),
		disks:      make(map[string]*computepb.Disk),
		images:     make(map[string]*computepb.Image),
	}
	s.registerRoutes()
	s.srv = httptest.NewServer(s.mux)
	return s
}

// URL returns the base HTTP URL of the mock server.
func (s *Server) URL() string {
	return s.srv.URL
}

// AddImage registers an image on the mock server.
func (s *Server) AddImage(project string, img *computepb.Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s/%s", project, img.GetName())
	s.images[key] = img
}

// GetInstance returns an instance by project, zone, and name, or an error if not found.
func (s *Server) GetInstance(project, zone, name string) (*computepb.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s/%s/%s", project, zone, name)
	inst, ok := s.instances[key]
	if !ok {
		return nil, fmt.Errorf("instance %q not found", key)
	}
	return inst, nil
}

// Close shuts down the mock server.
func (s *Server) Close() {
	s.srv.Close()
}

// ClientOptions returns the google.golang.org/api/option ClientOptions
// needed to configure compute clients to talk to this mock server.
func (s *Server) ClientOptions() []option.ClientOption {
	return []option.ClientOption{
		option.WithEndpoint(s.srv.URL),
		option.WithoutAuthentication(),
		option.WithHTTPClient(s.srv.Client()),
	}
}

func (s *Server) nextOpName() string {
	id := atomic.AddUint64(&s.opCounter, 1)
	return fmt.Sprintf("operation-%d", id)
}

func ptr[T any](v T) *T { return &v }
