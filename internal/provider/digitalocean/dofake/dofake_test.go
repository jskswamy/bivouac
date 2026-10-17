package dofake_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/digitalocean/dofake"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHVWo1pKvvMXUH3PfbyT2C8VQnQ6pDpn3Fz8F1rfbNkA test@fake"

func seeded(t *testing.T) *dofake.Server {
	t.Helper()
	s := dofake.New(t)
	s.AddSize(godo.Size{Slug: "s-2vcpu-4gb", Disk: 80, Regions: []string{"blr1"}, Available: true, PriceHourly: 0.03571, PriceMonthly: 24})
	return s
}

func TestFake_CreateGetDestroyDroplet(t *testing.T) {
	s := seeded(t)
	key := s.AddKey(godo.Key{Name: "laptop", PublicKey: testKey})
	p := s.Provider(t)
	ctx := context.Background()

	vm, err := p.Create(ctx, provider.InstanceSpec{
		Name: "dev", Region: "blr1", Size: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64",
		SSHKeys: []string{strconv.Itoa(key.ID)}, UserData: "#!/bin/sh\n",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if vm.IP == "" || vm.Status != "active" || vm.Region != "blr1" || vm.Size != "s-2vcpu-4gb" {
		t.Errorf("Create = %+v, want active in blr1 with an IP", vm)
	}
	if vm.PriceHourly != 0.03571 {
		t.Errorf("PriceHourly = %v, want the seeded size's price", vm.PriceHourly)
	}

	got, err := p.Get(ctx, vm.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IP != vm.IP {
		t.Errorf("Get IP = %q, want %q", got.IP, vm.IP)
	}
	if len(s.Droplets()) != 1 {
		t.Errorf("Droplets() = %d, want 1", len(s.Droplets()))
	}

	if err := p.Destroy(ctx, vm.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := p.Get(ctx, vm.ID); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("Get after Destroy = %v, want ErrNotFound", err)
	}
	if len(s.Droplets()) != 0 {
		t.Errorf("Droplets() after Destroy = %d, want 0", len(s.Droplets()))
	}
}

func TestFake_CreateRejectsWhatDigitalOceanRejects(t *testing.T) {
	s := seeded(t)
	snap := s.AddImage(godo.Image{Name: "snap", Type: "snapshot", Regions: []string{"nyc3"}, MinDiskSize: 25})
	big := s.AddImage(godo.Image{Name: "big", Type: "snapshot", Regions: []string{"blr1"}, MinDiskSize: 160})
	p := s.Provider(t)
	ctx := context.Background()

	for name, spec := range map[string]provider.InstanceSpec{
		"unknown size":          {Name: "a", Region: "blr1", Size: "s-99", Image: "ubuntu-24-04-x64"},
		"image in other region": {Name: "b", Region: "blr1", Size: "s-2vcpu-4gb", Image: strconv.Itoa(snap.ID)},
		"image too big":         {Name: "c", Region: "blr1", Size: "s-2vcpu-4gb", Image: strconv.Itoa(big.ID)},
		"unknown image id":      {Name: "d", Region: "blr1", Size: "s-2vcpu-4gb", Image: "424242"},
		"unknown ssh key":       {Name: "e", Region: "blr1", Size: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64", SSHKeys: []string{"77"}},
	} {
		var errResp *godo.ErrorResponse
		if _, err := p.Create(ctx, spec); !errors.As(err, &errResp) || errResp.Response.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: Create = %v, want a 422", name, err)
		}
	}
	if n := len(s.Droplets()); n != 0 {
		t.Errorf("Droplets() = %d after rejected creates, want 0", n)
	}
}

func TestFake_ListImagesPaginates(t *testing.T) {
	s := seeded(t)
	for i := range 45 {
		s.AddImage(godo.Image{Name: fmt.Sprintf("bivouac-%02d", i), Type: "snapshot", Regions: []string{"blr1"}, Tags: []string{"bivouac-image"}})
	}
	s.AddImage(godo.Image{Name: "untagged", Type: "snapshot", Regions: []string{"blr1"}})

	imgs, err := s.Provider(t).ListImages(context.Background(), "bivouac-image")
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(imgs) != 45 {
		t.Errorf("ListImages = %d images, want 45", len(imgs))
	}
	seen := map[string]bool{}
	for _, img := range imgs {
		if seen[img.ID] {
			t.Errorf("image %s listed twice", img.ID)
		}
		seen[img.ID] = true
	}

	pages := 0
	for _, r := range s.Requests() {
		if r.Method == http.MethodGet && r.Path == "/v2/images" {
			q, _ := url.ParseQuery(r.Query)
			if q.Get("tag_name") != "bivouac-image" {
				t.Errorf("listing query %q lacks tag_name", r.Query)
			}
			pages++
		}
	}
	if pages < 2 {
		t.Errorf("fetched %d pages, want more than one", pages)
	}
}

func TestFake_ListUserImagesIsPrivateOnly(t *testing.T) {
	s := seeded(t)
	s.AddImage(godo.Image{Name: "ubuntu", Type: "base", Slug: "ubuntu-24-04-x64", Public: true, Regions: []string{"blr1"}})
	mine := s.AddImage(godo.Image{Name: "mine", Type: "snapshot", Regions: []string{"blr1"}})

	imgs, err := s.Provider(t).ListImages(context.Background(), "")
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(imgs) != 1 || imgs[0].ID != strconv.Itoa(mine.ID) {
		t.Errorf("ListImages(\"\") = %+v, want only the private image", imgs)
	}
}

func TestFake_TagImageThroughTagsAPI(t *testing.T) {
	s := seeded(t)
	img := s.AddImage(godo.Image{Name: "snap", Type: "snapshot", Regions: []string{"blr1"}, Tags: []string{"bivouac-image"}})
	p := s.Provider(t)
	ctx := context.Background()

	if err := p.TagImage(ctx, strconv.Itoa(img.ID), "my-dev"); err != nil {
		t.Fatalf("TagImage: %v", err)
	}
	// A second tag of the same image must stay idempotent, as the
	// provider assumes of DigitalOcean.
	if err := p.TagImage(ctx, strconv.Itoa(img.ID), "my-dev"); err != nil {
		t.Fatalf("TagImage again: %v", err)
	}
	got := s.Images()[0]
	if !slices.Equal(got.Tags, []string{"bivouac-image", "my-dev"}) {
		t.Errorf("tags = %v, want [bivouac-image my-dev]", got.Tags)
	}

	imgs, err := p.ListImages(ctx, "my-dev")
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(imgs) != 1 || imgs[0].ID != strconv.Itoa(img.ID) {
		t.Errorf("ListImages(my-dev) = %+v, want the tagged image", imgs)
	}
}

func TestFake_TagMissingImageFails(t *testing.T) {
	s := seeded(t)
	if err := s.Provider(t).TagImage(context.Background(), "31337", "my-dev"); err == nil {
		t.Error("TagImage of a missing image succeeded")
	}
}

func TestFake_DeleteMissingImageIsNotFound(t *testing.T) {
	s := seeded(t)
	img := s.AddImage(godo.Image{Name: "snap", Type: "snapshot", Regions: []string{"blr1"}})
	p := s.Provider(t)
	ctx := context.Background()
	id := strconv.Itoa(img.ID)

	if err := p.DeleteImage(ctx, id); err != nil {
		t.Fatalf("first DeleteImage: %v", err)
	}
	if err := p.DeleteImage(ctx, id); !errors.Is(err, provider.ErrImageNotFound) {
		t.Errorf("second DeleteImage = %v, want ErrImageNotFound", err)
	}
	if _, err := p.GetImage(ctx, id); !errors.Is(err, provider.ErrImageNotFound) {
		t.Errorf("GetImage after delete = %v, want ErrImageNotFound", err)
	}
	if n := len(s.Images()); n != 0 {
		t.Errorf("Images() = %d, want 0", n)
	}
}

func TestFake_FailInjects(t *testing.T) {
	s := seeded(t)
	img := s.AddImage(godo.Image{Name: "snap", Type: "snapshot", Regions: []string{"blr1"}})
	p := s.Provider(t)
	ctx := context.Background()
	id := strconv.Itoa(img.ID)

	s.Fail(http.MethodDelete, "/v2/images/{id}", http.StatusForbidden, 1)
	if err := p.DeleteImage(ctx, id); !errors.Is(err, provider.ErrForbidden) {
		t.Fatalf("first DeleteImage = %v, want ErrForbidden", err)
	}
	if n := len(s.Images()); n != 1 {
		t.Fatalf("a refused delete removed the image")
	}
	if err := p.DeleteImage(ctx, id); err != nil {
		t.Errorf("second DeleteImage: %v", err)
	}
}

func TestFake_FailOnlyMatchesItsMethod(t *testing.T) {
	s := seeded(t)
	img := s.AddImage(godo.Image{Name: "snap", Type: "snapshot", Regions: []string{"blr1"}})
	p := s.Provider(t)
	s.Fail(http.MethodDelete, "/v2/images/{id}", http.StatusForbidden, 1)
	if _, err := p.GetImage(context.Background(), strconv.Itoa(img.ID)); err != nil {
		t.Errorf("GetImage hit a DELETE failure: %v", err)
	}
}

func TestFake_RateLimitCarriesDigitalOceansHeaders(t *testing.T) {
	s := seeded(t)
	s.Fail(http.MethodGet, "/v2/account", http.StatusTooManyRequests, 1)

	resp, err := http.Get(s.URL() + "/v2/account")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	// DigitalOcean's own body, so a caller that surfaces the API's
	// message says what the real API would have said.
	var body struct{ ID, Message string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.ID != "too_many_requests" || !strings.HasPrefix(body.Message, "API Rate limit exceeded.") {
		t.Errorf("body = %+v, want DigitalOcean's too_many_requests message", body)
	}
	for _, h := range []string{"Retry-After", "Ratelimit-Limit", "Ratelimit-Remaining", "Ratelimit-Reset"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("429 lacks %s", h)
		}
	}
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
}

func TestFake_BeforeRunsOnceBeforeTheMatchingRequest(t *testing.T) {
	s := seeded(t)
	img := s.AddImage(godo.Image{Name: "snap", Type: "snapshot", Regions: []string{"blr1"}})
	p := s.Provider(t)
	ctx := context.Background()
	runs := 0
	s.Before(http.MethodDelete, "/v2/images/{id}", func(s *dofake.Server) {
		runs++
		s.RemoveImage(img.ID)
	})

	if err := p.DeleteImage(ctx, strconv.Itoa(img.ID)); !errors.Is(err, provider.ErrImageNotFound) {
		t.Errorf("DeleteImage after another actor's delete = %v, want ErrImageNotFound", err)
	}
	_ = p.DeleteImage(ctx, strconv.Itoa(img.ID))
	if runs != 1 {
		t.Errorf("hook ran %d times, want 1", runs)
	}
}

// recordingTB stands in for the test that owns a fake, so that what the
// fake reports at cleanup can be inspected instead of failing this test.
type recordingTB struct {
	testing.TB
	errors   []string
	cleanups []func()
}

func (r *recordingTB) Helper()                   {}
func (r *recordingTB) Cleanup(f func())          { r.cleanups = append(r.cleanups, f) }
func (r *recordingTB) Errorf(f string, a ...any) { r.errors = append(r.errors, fmt.Sprintf(f, a...)) }

func (r *recordingTB) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestFake_UnknownRouteFailsTest(t *testing.T) {
	rec := &recordingTB{TB: t}
	s := dofake.New(rec)

	resp, err := authedDo(s, http.MethodGet, "/v2/volumes")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
	// A known path with a method the fake does not model is just as
	// unknown as a path it has never heard of.
	if resp, err := authedDo(s, http.MethodPatch, "/v2/images/1"); err == nil {
		_ = resp.Body.Close()
	}

	rec.runCleanups()
	if len(rec.errors) != 2 {
		t.Fatalf("reported %d errors, want 2: %q", len(rec.errors), rec.errors)
	}
	if !strings.Contains(rec.errors[0], "GET /v2/volumes") || !strings.Contains(rec.errors[1], "PATCH /v2/images/1") {
		t.Errorf("errors = %q, want both unknown requests named", rec.errors)
	}
}

// authedDo sends a request carrying a token, as the provider does.
func authedDo(s *dofake.Server, method, path string) (*http.Response, error) {
	req, err := http.NewRequest(method, s.URL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer dofake-token")
	return http.DefaultClient.Do(req)
}

func TestFake_UnauthenticatedProbeIsNotReported(t *testing.T) {
	rec := &recordingTB{TB: t}
	s := dofake.New(rec)

	resp, err := http.Get(s.URL() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	rec.runCleanups()
	if len(rec.errors) != 0 {
		t.Errorf("reported %q for a request without a token", rec.errors)
	}
}

func TestFake_KnownRoutesDoNotFailTest(t *testing.T) {
	rec := &recordingTB{TB: t}
	s := dofake.New(rec)
	if _, err := s.Provider(t).ListImages(context.Background(), ""); err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	rec.runCleanups()
	if len(rec.errors) != 0 {
		t.Errorf("reported %q for known routes", rec.errors)
	}
}

func TestFake_SnapshotPendingThenAvailable(t *testing.T) {
	s := seeded(t)
	p := s.Provider(t)
	ctx := context.Background()
	vm, err := p.Create(ctx, provider.InstanceSpec{Name: "builder", Region: "blr1", Size: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id, _ := strconv.Atoi(vm.ID)

	s.SetSnapshotPending(2)
	snap, err := s.Snapshot(id, "bivouac-my-dev-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Type != "snapshot" || !slices.Equal(snap.Regions, []string{"blr1"}) || snap.MinDiskSize != 80 {
		t.Errorf("snapshot = %+v, want a blr1 snapshot of an 80 GB disk", snap)
	}

	var got []string
	for range 3 {
		img, err := p.GetImage(ctx, strconv.Itoa(snap.ID))
		if err != nil {
			t.Fatalf("GetImage: %v", err)
		}
		got = append(got, img.Status)
	}
	if want := []string{"pending", "pending", "available"}; !slices.Equal(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

func TestFake_SnapshotOfMissingDropletFails(t *testing.T) {
	s := seeded(t)
	if _, err := s.Snapshot(1, "x"); err == nil {
		t.Error("Snapshot of a missing droplet succeeded")
	}
}

// TestFake_PackerDropletActions drives the routes the Packer plugin uses
// and bivouac's provider does not, through a bare godo client.
func TestFake_PackerDropletActions(t *testing.T) {
	s := seeded(t)
	c := godo.NewFromToken("fake")
	base, _ := url.Parse(s.URL() + "/")
	c.BaseURL = base
	ctx := context.Background()

	d, _, err := c.Droplets.Create(ctx, &godo.DropletCreateRequest{
		Name: "packer-1", Region: "blr1", Size: "s-2vcpu-4gb",
		Image: godo.DropletCreateImage{Slug: "ubuntu-24-04-x64"}, Tags: []string{"bivouac-builder"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a, _, err := c.DropletActions.PowerOff(ctx, d.ID)
	if err != nil {
		t.Fatalf("PowerOff: %v", err)
	}
	if a.Status != godo.ActionCompleted || a.ResourceID != d.ID {
		t.Errorf("power_off action = %+v", a)
	}
	if got, _, _ := c.Droplets.Get(ctx, d.ID); got.Status != "off" {
		t.Errorf("status after power_off = %q, want off", got.Status)
	}

	a, _, err = c.DropletActions.Snapshot(ctx, d.ID, "bivouac-my-dev-2")
	if err != nil {
		t.Fatalf("Snapshot action: %v", err)
	}
	if a, _, err = c.DropletActions.Get(ctx, d.ID, a.ID); err != nil || a.Status != godo.ActionCompleted {
		t.Fatalf("droplet action get = %+v, %v", a, err)
	}
	if a, _, err = c.Actions.Get(ctx, a.ID); err != nil || a.Type != "snapshot" {
		t.Fatalf("action get = %+v, %v", a, err)
	}

	// How the plugin finds the snapshot it just asked for, by name.
	snaps, _, err := c.Droplets.Snapshots(ctx, d.ID, nil)
	if err != nil || len(snaps) != 1 || snaps[0].Name != "bivouac-my-dev-2" || snaps[0].Type != "snapshot" {
		t.Fatalf("droplet snapshots = %+v, %v", snaps, err)
	}

	got, _, err := c.Droplets.Get(ctx, d.ID)
	if err != nil || len(got.SnapshotIDs) != 1 {
		t.Fatalf("droplet snapshot ids = %v, %v", got.SnapshotIDs, err)
	}
	img, _, err := c.Images.GetByID(ctx, got.SnapshotIDs[0])
	if err != nil || img.Name != "bivouac-my-dev-2" || img.Type != "snapshot" {
		t.Fatalf("snapshot image = %+v, %v", img, err)
	}

	tagged, _, err := c.Droplets.ListByTag(ctx, "bivouac-builder", nil)
	if err != nil || len(tagged) != 1 {
		t.Errorf("ListByTag = %d droplets, %v; want 1", len(tagged), err)
	}

	if _, _, err := c.Images.Update(ctx, img.ID, &godo.ImageUpdateRequest{Name: "renamed"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if s.Images()[0].Name != "renamed" {
		t.Errorf("name after update = %q", s.Images()[0].Name)
	}

	if _, _, err := c.Account.Get(ctx); err != nil {
		t.Errorf("Account.Get: %v", err)
	}
	if regions, _, err := c.Regions.List(ctx, nil); err != nil || len(regions) == 0 {
		t.Errorf("Regions.List = %v, %v", regions, err)
	}
	if _, err := c.Droplets.Delete(ctx, d.ID); err != nil {
		t.Errorf("Delete: %v", err)
	}
}

func TestFake_KeysRoundTripAndPaginate(t *testing.T) {
	s := seeded(t)
	for i := range 3 {
		s.AddKey(godo.Key{Name: fmt.Sprintf("k%d", i), PublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("key-%d", i)))})
	}
	p := s.Provider(t)
	ctx := context.Background()

	created, err := p.CreateKey(ctx, "laptop", testKey)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if created.Fingerprint == "" || created.ID == "" {
		t.Errorf("CreateKey = %+v, want an ID and fingerprint", created)
	}
	if _, err := p.CreateKey(ctx, "again", testKey); err == nil {
		t.Error("registering the same public key twice succeeded")
	}

	keys, err := p.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 4 || len(s.Keys()) != 4 {
		t.Errorf("ListKeys = %d, Keys() = %d, want 4", len(keys), len(s.Keys()))
	}

	c := godo.NewFromToken("fake")
	base, _ := url.Parse(s.URL() + "/")
	c.BaseURL = base
	if k, _, err := c.Keys.GetByFingerprint(ctx, created.Fingerprint); err != nil || k.Name != "laptop" {
		t.Errorf("GetByFingerprint = %+v, %v", k, err)
	}
	id, _ := strconv.Atoi(created.ID)
	if _, err := c.Keys.DeleteByID(ctx, id); err != nil {
		t.Errorf("DeleteByID: %v", err)
	}
	if _, _, err := c.Keys.GetByID(ctx, id); err == nil {
		t.Error("deleted key still readable")
	}
}

func TestFake_SizesFeedSizeDiskGB(t *testing.T) {
	s := seeded(t)
	got, err := s.Provider(t).SizeDiskGB(context.Background(), "s-2vcpu-4gb")
	if err != nil || got != 80 {
		t.Errorf("SizeDiskGB = %d, %v; want 80", got, err)
	}
}
