package image

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// snap is a ready, fully marked Bivouac snapshot built in blr1 for
// x86_64 with fish, carrying the user tag "my-dev".
func snap(id string, created time.Time) provider.Image {
	return provider.Image{
		ID:        id,
		Name:      "bivouac-" + id,
		Created:   created,
		Regions:   []string{"blr1"},
		MinDiskGB: 25,
		Status:    "available",
		Tags:      SnapshotTags("my-dev", "x86_64", "blr1", "fish"),
		Type:      "snapshot",
	}
}

func with(img provider.Image, f func(*provider.Image)) provider.Image {
	f(&img)
	return img
}

func without(tags []string, drop string) []string {
	var out []string
	for _, t := range tags {
		if t != drop {
			out = append(out, t)
		}
	}
	return out
}

func req(ref string) Request {
	return Request{Ref: ref, Arch: "x86_64", Region: "blr1", Size: "s-2vcpu-4gb", Shell: "fish"}
}

func newFake(imgs ...provider.Image) *fakeImages {
	return &fakeImages{images: imgs, disks: map[string]int{"s-2vcpu-4gb": 80, "s-1vcpu-1gb": 25}}
}

func TestResolve_StockSlugMakesNoCalls(t *testing.T) {
	f := newFake(snap("1", t0))
	got, err := Resolve(context.Background(), f, req("ubuntu-24-04-x64"))
	if err != nil {
		t.Fatal(err)
	}
	want := Resolution{Requested: "ubuntu-24-04-x64", Image: "ubuntu-24-04-x64"}
	if got != want {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
	if len(f.calls) != 0 {
		t.Fatalf("stock slug made provider calls: %v", f.calls)
	}
}

func TestResolve_StockSlugWithNilManager(t *testing.T) {
	got, err := Resolve(context.Background(), nil, req("ubuntu-24-04-x64"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != "ubuntu-24-04-x64" || got.Baked {
		t.Fatalf("Resolve = %+v", got)
	}
}

func TestResolve_Tag(t *testing.T) {
	cases := []struct {
		name   string
		images []provider.Image
		want   string // resolved ID; "" means an error is expected
		errHas []string
	}{
		{
			name:   "newest by created",
			images: []provider.Image{snap("100", t0), snap("90", t0.Add(time.Hour)), snap("80", t0.Add(-time.Hour))},
			want:   "90",
		},
		{
			name:   "tie broken by the higher ID",
			images: []provider.Image{snap("9", t0), snap("100", t0), snap("20", t0)},
			want:   "100",
		},
		{
			name: "arch mismatch is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) {
					i.Tags = SnapshotTags("my-dev", "arm64", "blr1", "fish")
				}),
			},
			want: "1",
		},
		{
			name: "launch region not in Regions is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) { i.Regions = []string{"nyc1"} }),
			},
			want: "1",
		},
		{
			name: "pending is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) { i.Status = "pending" }),
			},
			want: "1",
		},
		{
			name: "missing marker is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) { i.Tags = without(i.Tags, "bivouac-shell-fish") }),
			},
			want: "1",
		},
		{
			name: "foreign image carrying the user tag is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) { i.Tags = []string{"my-dev"} }),
			},
			want: "1",
		},
		{
			name: "non-snapshot is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) { i.Type = "custom" }),
			},
			want: "1",
		},
		{
			name: "other format version is skipped",
			images: []provider.Image{
				snap("1", t0),
				with(snap("2", t0.Add(time.Hour)), func(i *provider.Image) {
					i.Tags = append(without(i.Tags, FormatTag()), "bivouac-format-2")
				}),
			},
			want: "1",
		},
		{
			name: "none eligible names tag, arch and region",
			images: []provider.Image{
				with(snap("1", t0), func(i *provider.Image) { i.Status = "pending" }),
				with(snap("2", t0), func(i *provider.Image) { i.Tags = []string{"my-dev"} }),
			},
			errHas: []string{"my-dev", "x86_64", "blr1"},
		},
		{
			name:   "min disk larger than the size's disk",
			images: []provider.Image{with(snap("1", t0), func(i *provider.Image) { i.MinDiskGB = 50 })},
			errHas: []string{"50", "25", "s-1vcpu-1gb"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(tc.images...)
			r := req("tag:my-dev")
			if strings.Contains(tc.name, "min disk") {
				r.Size = "s-1vcpu-1gb"
			}
			got, err := Resolve(context.Background(), f, r)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("Resolve = %+v, want error", got)
				}
				for _, s := range tc.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := Resolution{Requested: "tag:my-dev", Image: tc.want, Baked: true, Shell: "fish"}
			if got != want {
				t.Fatalf("Resolve = %+v, want %+v", got, want)
			}
		})
	}
}

func TestResolve_BakedShellComesFromMarkers(t *testing.T) {
	img := with(snap("7", t0), func(i *provider.Image) {
		i.Tags = SnapshotTags("my-dev", "x86_64", "blr1", "zsh")
	})
	got, err := Resolve(context.Background(), newFake(img), req("tag:my-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Shell != "zsh" {
		t.Fatalf("Shell = %q, want zsh", got.Shell)
	}
}

func TestResolve_NumericID(t *testing.T) {
	f := newFake(snap("123456", t0))
	got, err := Resolve(context.Background(), f, req("123456"))
	if err != nil {
		t.Fatal(err)
	}
	want := Resolution{Requested: "123456", Image: "123456", Baked: true, Shell: "fish"}
	if got != want {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
}

func TestResolve_NumericIDWithoutUserTag(t *testing.T) {
	// An ID names the image directly, so the user tag is not required.
	img := with(snap("5", t0), func(i *provider.Image) { i.Tags = without(i.Tags, "my-dev") })
	if _, err := Resolve(context.Background(), newFake(img), req("5")); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_NumericIDForeign(t *testing.T) {
	img := with(snap("5", t0), func(i *provider.Image) { i.Tags = nil })
	_, err := Resolve(context.Background(), newFake(img), req("5"))
	if err == nil || !strings.Contains(err.Error(), "5") {
		t.Fatalf("err = %v, want an error naming the ID", err)
	}
}

func TestResolve_NumericIDRegionMismatchNamesBoth(t *testing.T) {
	img := with(snap("5", t0), func(i *provider.Image) { i.Regions = []string{"nyc1"} })
	_, err := Resolve(context.Background(), newFake(img), req("5"))
	if err == nil || !strings.Contains(err.Error(), "blr1") || !strings.Contains(err.Error(), "nyc1") {
		t.Fatalf("err = %v, want both regions named", err)
	}
}

// A tag built only elsewhere is the likeliest cause of "no image": the
// error has to say where the tagged builds are, not just where it looked.
func TestResolve_TagRegionMismatchNamesBoth(t *testing.T) {
	nyc := with(snap("7", t0), func(i *provider.Image) {
		i.Regions = []string{"nyc3"}
		i.Tags = SnapshotTags("my-dev", "x86_64", "nyc3", "fish")
	})
	_, err := Resolve(context.Background(), newFake(nyc), req("tag:my-dev"))
	if err == nil || !strings.Contains(err.Error(), "blr1") || !strings.Contains(err.Error(), "nyc3") || !strings.Contains(err.Error(), "7") {
		t.Fatalf("err = %v, want both regions and the skipped image named", err)
	}
}

func TestResolve_TagSkippedListIsBounded(t *testing.T) {
	var imgs []provider.Image
	for i := 1; i <= 8; i++ {
		imgs = append(imgs, with(snap(fmt.Sprint(i), t0.Add(time.Duration(i)*time.Hour)), func(i *provider.Image) { i.Status = "pending" }))
	}
	_, err := Resolve(context.Background(), newFake(imgs...), req("tag:my-dev"))
	if err == nil || !strings.Contains(err.Error(), "image 8") || strings.Contains(err.Error(), "image 1 ") || !strings.Contains(err.Error(), "5 more") {
		t.Fatalf("err = %v, want the newest three named and the rest counted", err)
	}
}

func TestResolve_NumericIDGone(t *testing.T) {
	_, err := Resolve(context.Background(), newFake(), req("424242"))
	if !errors.Is(err, provider.ErrImageNotFound) {
		t.Fatalf("err = %v, want ErrImageNotFound", err)
	}
}

func TestResolve_NumericIDMinDisk(t *testing.T) {
	img := with(snap("5", t0), func(i *provider.Image) { i.MinDiskGB = 50 })
	r := req("5")
	r.Size = "s-1vcpu-1gb"
	_, err := Resolve(context.Background(), newFake(img), r)
	if err == nil || !strings.Contains(err.Error(), "50") || !strings.Contains(err.Error(), "s-1vcpu-1gb") {
		t.Fatalf("err = %v, want min disk error", err)
	}
}

func TestResolve_CustomRefWithoutManager(t *testing.T) {
	for _, ref := range []string{"tag:my-dev", "123"} {
		_, err := Resolve(context.Background(), nil, req(ref))
		if err == nil || !strings.Contains(err.Error(), "provider does not support custom images") {
			t.Errorf("%s: err = %v", ref, err)
		}
	}
}

func TestResolve_EmptyTagName(t *testing.T) {
	f := newFake(snap("1", t0))
	if _, err := Resolve(context.Background(), f, req("tag:")); err == nil {
		t.Fatal("tag: with no name resolved")
	}
	if len(f.calls) != 0 {
		t.Fatalf("empty tag made provider calls: %v", f.calls)
	}
}

func TestEligible_RespectsScopeTag(t *testing.T) {
	img := snap("1", t0)
	if !Eligible(img, Scope{Tag: "my-dev", Arch: "x86_64", Region: "blr1"}) {
		t.Fatal("matching image not eligible")
	}
	if Eligible(img, Scope{Tag: "other", Arch: "x86_64", Region: "blr1"}) {
		t.Fatal("image without the scope tag was eligible")
	}
	if !Eligible(img, Scope{Arch: "x86_64", Region: "blr1"}) {
		t.Fatal("empty scope tag should not filter")
	}
}

func TestSortNewest(t *testing.T) {
	imgs := []provider.Image{snap("9", t0), snap("100", t0), snap("5", t0.Add(time.Hour))}
	SortNewest(imgs)
	var ids []string
	for _, i := range imgs {
		ids = append(ids, i.ID)
	}
	if strings.Join(ids, ",") != "5,100,9" {
		t.Fatalf("order = %v", ids)
	}
}

// TagRef is what build writes into a config and image tag prints; it has
// to be exactly what Resolve reads back as a tag reference.
func TestTagRef_RoundTripsThroughResolve(t *testing.T) {
	tag, ok := strings.CutPrefix(TagRef("my-dev"), tagRefPrefix)
	if !ok || tag != "my-dev" {
		t.Fatalf("TagRef(%q) = %q, want the %q prefix Resolve reads", "my-dev", TagRef("my-dev"), tagRefPrefix)
	}
}
