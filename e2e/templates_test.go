package e2e

import (
	"net"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"testing"
)

// serveTemplates serves this checkout's committed templates/ flake to the
// VMs over git's smart HTTP protocol on the bridge address, and returns
// the template reference a config uses to build from it.
//
// The built-in "python" template floats on GitHub's default branch, so a
// test using it would exercise whatever is published there rather than
// the templates under review -- and a broken pin on that branch (a
// release tarball its CDN stopped serving) would fail the run for a
// reason this checkout may already have fixed. A full reference is the
// path a config with its own template takes; without packages or modules
// to render, the image build and launch use it exactly as they use the
// built-in one's expansion.
//
// Only committed templates are served: a clone sees HEAD, not the
// working tree.
func serveTemplates(t *testing.T, dir string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	run(t, "", nil, "git", "clone", "--bare", "--quiet", root, filepath.Join(dir, "bivouac.git"))

	ln, err := net.Listen("tcp", gatewayIP+":0")
	if err != nil {
		t.Fatalf("listening on the bridge for the template server: %v", err)
	}
	srv := &http.Server{Handler: &cgi.Handler{
		Path: git,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1"},
	}}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "git+http://" + ln.Addr().String() + "/bivouac.git?dir=templates#python-x86_64-linux"
}
