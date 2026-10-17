package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
)

// Image commands reach this capability by type assertion, so the
// assertion has to hold.
func TestProvider_IsAnImageManager(t *testing.T) {
	var p any = &Provider{}
	if _, ok := p.(provider.ImageManager); !ok {
		t.Fatal("*Provider does not implement provider.ImageManager")
	}
}

// Resolution and pruning both pick from the complete set; an image on a
// later page that went unseen could be the newest, or one to keep.
func TestListImages_AllPages(t *testing.T) {
	for _, tc := range []struct {
		name, tag, wantQuery, wantValue string
	}{
		{name: "by tag", tag: "my-dev", wantQuery: "tag_name", wantValue: "my-dev"},
		{name: "all private", tag: "", wantQuery: "private", wantValue: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, mux := newTestProvider(t)
			mux.HandleFunc("/v2/images", func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get(tc.wantQuery); got != tc.wantValue {
					t.Errorf("query %s = %q, want %q (url %s)", tc.wantQuery, got, tc.wantValue, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("page") == "2" {
					_, _ = w.Write([]byte(`{"images":[{"id":3},{"id":4}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"images":[{"id":1},{"id":2}],"links":{"pages":{"next":"https://api.digitalocean.com/v2/images?page=2"}}}`))
			})

			imgs, err := p.ListImages(context.Background(), tc.tag)
			if err != nil {
				t.Fatalf("ListImages() error = %v", err)
			}
			if len(imgs) != 4 || imgs[0].ID != "1" || imgs[3].ID != "4" {
				t.Fatalf("ListImages() = %+v, want IDs 1..4", imgs)
			}
		})
	}
}

// A foreign image (not a snapshot, or without markers) is still returned:
// the caller decides eligibility, so every field it decides on must
// survive the mapping.
func TestListImages_MapsFields(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"images":[
			{"id":123456,"name":"bivouac-x","type":"snapshot","regions":["blr1","nyc3"],"min_disk_size":50,
			 "status":"available","tags":["bivouac-image","my-dev"],"created_at":"2026-10-17T08:30:00Z"},
			{"id":7,"name":"someone-else","type":"custom","status":"pending"}
		]}`))
	})

	imgs, err := p.ListImages(context.Background(), "")
	if err != nil {
		t.Fatalf("ListImages() error = %v", err)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d images, want 2", len(imgs))
	}
	got := imgs[0]
	if got.ID != "123456" || got.Name != "bivouac-x" || got.Type != "snapshot" || got.Status != "available" || got.MinDiskGB != 50 {
		t.Errorf("image = %+v", got)
	}
	if strings.Join(got.Regions, ",") != "blr1,nyc3" {
		t.Errorf("Regions = %v", got.Regions)
	}
	if strings.Join(got.Tags, ",") != "bivouac-image,my-dev" {
		t.Errorf("Tags = %v", got.Tags)
	}
	if want := time.Date(2026, 10, 17, 8, 30, 0, 0, time.UTC); !got.Created.Equal(want) {
		t.Errorf("Created = %v, want %v", got.Created, want)
	}
	if foreign := imgs[1]; foreign.Type != "custom" || foreign.Status != "pending" || len(foreign.Tags) != 0 {
		t.Errorf("foreign image = %+v", foreign)
	}
}

func TestGetImage(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images/123456", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image":{"id":123456,"type":"snapshot","status":"available","min_disk_size":25}}`))
	})
	img, err := p.GetImage(context.Background(), "123456")
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	if img.ID != "123456" || img.MinDiskGB != 25 {
		t.Errorf("image = %+v", img)
	}
}

func TestGetImage_NotFound(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"The resource you requested could not be found."}`))
	})
	_, err := p.GetImage(context.Background(), "99")
	if !errors.Is(err, provider.ErrImageNotFound) {
		t.Fatalf("GetImage() error = %v, want ErrImageNotFound", err)
	}
}

// Images.Update cannot add tags; the tag has to be created and then
// attached through the tags API.
func TestTagImage_UsesTagResources(t *testing.T) {
	p, mux := newTestProvider(t)
	var created bool
	var resources []map[string]any
	mux.HandleFunc("/v2/tags", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "my-dev" {
			t.Errorf("tag create name = %q, want my-dev", body["name"])
		}
		created = true
		// The tag already existing must not stop the attach.
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"id":"unprocessable_entity","message":"tag already exists"}`))
	})
	mux.HandleFunc("/v2/tags/my-dev/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body struct {
			Resources []map[string]any `json:"resources"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		resources = body.Resources
		w.WriteHeader(http.StatusNoContent)
	})

	if err := p.TagImage(context.Background(), "123456", "my-dev"); err != nil {
		t.Fatalf("TagImage() error = %v", err)
	}
	if !created {
		t.Error("tag was not created first")
	}
	if len(resources) != 1 || resources[0]["resource_type"] != "image" || resources[0]["resource_id"] != "123456" {
		t.Errorf("resources = %v, want one image 123456", resources)
	}
}

func TestDeleteImage(t *testing.T) {
	p, mux := newTestProvider(t)
	var deleted bool
	mux.HandleFunc("/v2/images/123456", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	if err := p.DeleteImage(context.Background(), "123456"); err != nil {
		t.Fatalf("DeleteImage() error = %v", err)
	}
	if !deleted {
		t.Error("no DELETE was sent")
	}
}

// Prune treats an image someone else already deleted as done, so a 404
// has to be recognisable.
func TestDeleteImage_NotFoundIsErrImageNotFound(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"not found"}`))
	})
	err := p.DeleteImage(context.Background(), "99")
	if !errors.Is(err, provider.ErrImageNotFound) {
		t.Fatalf("DeleteImage() error = %v, want ErrImageNotFound", err)
	}
}

func TestDeleteImage_ForbiddenIsErrForbidden(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"id":"forbidden","message":"token missing required scope"}`))
	})
	err := p.DeleteImage(context.Background(), "99")
	if !provider.IsForbidden(err) {
		t.Fatalf("DeleteImage() error = %v, want forbidden", err)
	}
}

func TestSizeDiskGB(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"sizes":[{"slug":"s-2vcpu-4gb","disk":80}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"sizes":[{"slug":"s-1vcpu-1gb","disk":25}],"links":{"pages":{"next":"https://api.digitalocean.com/v2/sizes?page=2"}}}`))
	})

	disk, err := p.SizeDiskGB(context.Background(), "s-2vcpu-4gb")
	if err != nil {
		t.Fatalf("SizeDiskGB() error = %v", err)
	}
	if disk != 80 {
		t.Errorf("disk = %d, want 80", disk)
	}

	_, err = p.SizeDiskGB(context.Background(), "s-nope")
	if err == nil || !strings.Contains(err.Error(), "s-nope") {
		t.Errorf("unknown slug error = %v, want it to name s-nope", err)
	}
}

// createBody runs Create against a droplet that is immediately ready and
// returns the decoded request body.
func createBody(t *testing.T, image string) map[string]any {
	t.Helper()
	p, mux := newTestProvider(t)
	var body map[string]any
	ready := `{"droplet":{"id":1,"status":"active","networks":{"v4":[{"ip_address":"203.0.113.5","type":"public"}]}}}`
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(ready))
	})
	mux.HandleFunc("/v2/droplets/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ready))
	})
	if _, err := p.Create(context.Background(), provider.InstanceSpec{Name: "n", Region: "blr1", Size: "s-1vcpu-1gb", Image: image}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return body
}

// godo sends the numeric form only when ID is set and Slug is empty; a
// snapshot ID passed as a slug is rejected by the API.
func TestCreate_NumericImageSendsID(t *testing.T) {
	got := createBody(t, "123456")["image"]
	if n, ok := got.(float64); !ok || n != 123456 {
		t.Errorf("image = %#v, want 123456", got)
	}
}

func TestCreate_SlugImageSendsSlug(t *testing.T) {
	if got := createBody(t, "ubuntu-24-04-x64")["image"]; got != "ubuntu-24-04-x64" {
		t.Errorf("image = %#v, want \"ubuntu-24-04-x64\"", got)
	}
}
