package cmd

// Contract tests: the real commands and the real digitalocean.Provider
// against dofake, the stateful fake DigitalOcean API. The command-level
// tests elsewhere in this package stub the provider interface, so they
// cannot catch a request the client builds wrongly, a status the
// provider maps wrongly, or a page it never reads. These assert on what
// crossed the wire and on the fake's state, not on what the provider
// returned.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean/dofake"
	"github.com/jskswamy/bivouac/internal/testenv"
)

// nixInstaller is what only the stock boot script runs: a baked image
// already carries Nix.
const nixInstaller = "install.determinate.systems/nix"

// contractFake is a fake account offering the size every contract
// config launches, in both regions the tests build in.
func contractFake(t *testing.T) *dofake.Server {
	t.Helper()
	s := dofake.New(t)
	s.AddSize(godo.Size{Slug: "s-1vcpu-1gb", Disk: 25, Memory: 1024, Vcpus: 1, Regions: []string{"nyc3", "blr1"}, Available: true, PriceHourly: 0.00893, PriceMonthly: 6})
	return s
}

func fakeResolver(t *testing.T, s *dofake.Server) providerResolver {
	return func(context.Context) (provider.Provider, error) { return s.Provider(t), nil }
}

// seedSnap adds a ready bivouac snapshot of tag, built for fish in
// region, created hour hours into a fixed day so that order is explicit.
func seedSnap(s *dofake.Server, tag, region string, hour int) godo.Image {
	return s.AddImage(godo.Image{
		Name:         fmt.Sprintf("%s-%02d", tag, hour),
		Type:         "snapshot",
		Distribution: "Ubuntu",
		Regions:      []string{region},
		MinDiskSize:  25,
		Created:      time.Date(2026, 10, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Tags:         image.SnapshotTags(tag, "x86_64", region, "fish"),
	})
}

func requestsTo(s *dofake.Server, method, path string) []dofake.Request {
	var out []dofake.Request
	for _, r := range s.Requests() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func requestsUnder(s *dofake.Server, method, prefix string) []dofake.Request {
	var out []dofake.Request
	for _, r := range s.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// useAPINotice sends the API URL notice to a buffer for this test and
// lets it be printed again, as it would be in a new process.
func useAPINotice(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := apiNotice
	apiNotice = &onceNotice{w: &buf}
	t.Cleanup(func() { apiNotice = old })
	return &buf
}

func TestResolveProvider_HonoursAPIURL(t *testing.T) {
	s := contractFake(t)
	snap := seedSnap(s, "my-dev", "nyc3", 1)
	notice := useAPINotice(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "dummy-token")
	t.Setenv("DIGITALOCEAN_API_URL", s.URL())

	p, err := resolveProvider(context.Background())
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	m, ok := p.(provider.ImageManager)
	if !ok {
		t.Fatalf("provider %T does not manage images", p)
	}
	imgs, err := m.ListImages(context.Background(), "my-dev")
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(imgs) != 1 || imgs[0].ID != strconv.Itoa(snap.ID) {
		t.Errorf("ListImages = %+v, want the fake's image %d", imgs, snap.ID)
	}
	if len(requestsTo(s, "GET", "/v2/images")) == 0 {
		t.Error("the fake saw no listing; the provider went somewhere else")
	}

	// Asked again, as image build does for the provider and Packer, it
	// says so only once.
	if _, err := resolveProvider(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "Using DigitalOcean API at " + s.URL()
	if got := notice.String(); strings.Count(got, want) != 1 {
		t.Errorf("notice = %q, want %q exactly once", got, want)
	}
}

func TestResolveProvider_RejectsBadAPIURL(t *testing.T) {
	useAPINotice(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "dummy-token")
	t.Setenv("DIGITALOCEAN_API_URL", "::")

	_, err := resolveProvider(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DIGITALOCEAN_API_URL") {
		t.Fatalf("resolveProvider = %v, want an error naming DIGITALOCEAN_API_URL", err)
	}
}

// Without the variable the notice stays silent: it exists to make an
// override visible, not to narrate the normal case.
func TestResolveProvider_NoAPIURLIsSilent(t *testing.T) {
	notice := useAPINotice(t)
	t.Setenv("DIGITALOCEAN_TOKEN", "dummy-token")
	t.Setenv("DIGITALOCEAN_API_URL", "")

	if _, err := resolveProvider(context.Background()); err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if notice.Len() != 0 {
		t.Errorf("notice = %q, want nothing", notice.String())
	}
}

// contractSteps stands in for everything up does after the droplet
// exists: readiness, provisioning and the tailnet all need SSH to a real
// machine, which tier 1 does not have.
func contractSteps() lifecycle.Steps {
	return lifecycle.Steps{
		WaitReady:     func(context.Context, string, string, time.Duration) error { return nil },
		Reconcile:     func(context.Context, string, string) error { return nil },
		JoinTailscale: func(context.Context, string, string) error { return nil },
	}
}

// execContractUp runs `up fixture`, confirmed, in a fresh repository
// whose bivouac.pkl launches imageRef in nyc3 on s-1vcpu-1gb.
func execContractUp(t *testing.T, s *dofake.Server, imageRef string) (string, error) {
	t.Helper()
	testenv.Isolate(t)
	dir := initTestRepo(t)
	chdir(t, dir)
	minimalBivouacPkl(t, dir)
	path := filepath.Join(dir, "bivouac.pkl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, fmt.Appendf(nil, "image = %q\n", imageRef)...), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "up" {
			root.RemoveCommand(c)
		}
	}
	root.AddCommand(newUpCmdWith(fakeResolver(t, s), contractSteps()))
	root.SetArgs([]string{"up", "fixture"})
	root.SetIn(strings.NewReader("y\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	err = root.Execute()
	return out.String(), err
}

// createBody is the one droplet create up sent.
func createBody(t *testing.T, s *dofake.Server) map[string]json.RawMessage {
	t.Helper()
	creates := requestsTo(s, "POST", "/v2/droplets")
	if len(creates) != 1 {
		t.Fatalf("droplet creates = %d, want 1", len(creates))
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(creates[0].Body, &body); err != nil {
		t.Fatalf("create body is not JSON: %v\n%s", err, creates[0].Body)
	}
	return body
}

func userData(t *testing.T, body map[string]json.RawMessage) string {
	t.Helper()
	var ud string
	if err := json.Unmarshal(body["user_data"], &ud); err != nil {
		t.Fatalf("user_data: %v", err)
	}
	return ud
}

func noCreate(t *testing.T, s *dofake.Server) {
	t.Helper()
	if n := len(requestsTo(s, "POST", "/v2/droplets")); n != 0 {
		t.Errorf("droplet creates = %d, want none", n)
	}
	if n := len(s.Droplets()); n != 0 {
		t.Errorf("droplets on the account = %d, want none", n)
	}
}

func TestContractUp_TagSendsImageIDAndBakedScript(t *testing.T) {
	s := contractFake(t)
	seedSnap(s, "my-dev", "nyc3", 1)
	newer := seedSnap(s, "my-dev", "nyc3", 2)

	out, err := execContractUp(t, s, "tag:my-dev")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	body := createBody(t, s)
	// A number, not a string: DigitalOcean reads a string as a slug.
	if got := string(body["image"]); got != strconv.Itoa(newer.ID) {
		t.Errorf("create image = %s, want the newer snapshot's ID %d as a number", got, newer.ID)
	}
	if ud := userData(t, body); strings.Contains(ud, nixInstaller) {
		t.Errorf("baked launch runs the Nix installer:\n%s", ud)
	}
	droplets := s.Droplets()
	if len(droplets) != 1 || droplets[0].Image == nil || droplets[0].Image.ID != newer.ID {
		t.Errorf("droplets = %+v, want one booted from image %d", droplets, newer.ID)
	}
}

func TestContractUp_StockSlugSendsSlugAndFullScript(t *testing.T) {
	s := contractFake(t)
	seedSnap(s, "my-dev", "nyc3", 1)

	out, err := execContractUp(t, s, image.StockSourceX86)
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	body := createBody(t, s)
	if got, want := string(body["image"]), strconv.Quote(image.StockSourceX86); got != want {
		t.Errorf("create image = %s, want the slug %s", got, want)
	}
	if ud := userData(t, body); !strings.Contains(ud, nixInstaller) {
		t.Errorf("stock launch does not install Nix:\n%s", ud)
	}
	if got := requestsUnder(s, "GET", "/v2/images"); len(got) != 0 {
		t.Errorf("a stock slug consulted the image API: %+v", got)
	}
}

func TestContractUp_NumericIDInOtherRegionFailsBeforeCreate(t *testing.T) {
	s := contractFake(t)
	elsewhere := seedSnap(s, "my-dev", "blr1", 1)

	_, err := execContractUp(t, s, strconv.Itoa(elsewhere.ID))
	if err == nil || !strings.Contains(err.Error(), "nyc3") {
		t.Fatalf("up = %v, want a refusal naming the launch region nyc3", err)
	}
	if len(requestsTo(s, "GET", "/v2/images/"+strconv.Itoa(elsewhere.ID))) != 1 {
		t.Error("the ID was not looked up through the API")
	}
	noCreate(t, s)
}

func TestContractUp_TagBuiltElsewhereNamesRegion(t *testing.T) {
	s := contractFake(t)
	seedSnap(s, "my-dev", "blr1", 1)

	_, err := execContractUp(t, s, "tag:my-dev")
	if err == nil || !strings.Contains(err.Error(), "blr1") {
		t.Fatalf("up = %v, want an error naming blr1, where the tag was built", err)
	}
	noCreate(t, s)
}

func TestContractUp_SizeTooSmallFailsBeforeCreate(t *testing.T) {
	s := contractFake(t)
	big := s.AddImage(godo.Image{
		Name: "big", Type: "snapshot", Regions: []string{"nyc3"}, MinDiskSize: 50,
		Created: time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Tags:    image.SnapshotTags("my-dev", "x86_64", "nyc3", "fish"),
	})

	_, err := execContractUp(t, s, "tag:my-dev")
	if err == nil || !strings.Contains(err.Error(), "50 GB") || !strings.Contains(err.Error(), strconv.Itoa(big.ID)) {
		t.Fatalf("up = %v, want image %d's 50 GB disk named", err, big.ID)
	}
	if len(requestsTo(s, "GET", "/v2/sizes")) == 0 {
		t.Error("the size's disk was not read from the API")
	}
	noCreate(t, s)
}

func TestContractUp_ForbiddenListingIsReported(t *testing.T) {
	s := contractFake(t)
	seedSnap(s, "my-dev", "nyc3", 1)
	s.Fail("GET", "/v2/images", 403, 1)

	_, err := execContractUp(t, s, "tag:my-dev")
	if !errors.Is(err, provider.ErrForbidden) {
		t.Fatalf("up = %v, want provider.ErrForbidden", err)
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("error = %q, want it to say the token is not permitted", err)
	}
	noCreate(t, s)
}
