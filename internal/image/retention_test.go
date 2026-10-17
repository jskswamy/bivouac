package image

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
)

var blr = Scope{Tag: "my-dev", Arch: "x86_64", Region: "blr1"}

// five returns five eligible snapshots, ID 1 oldest through ID 5 newest.
func five() []provider.Image {
	var out []provider.Image
	for i := 1; i <= 5; i++ {
		out = append(out, snap(fmt.Sprint(i), t0.Add(time.Duration(i)*time.Hour)))
	}
	return out
}

func TestPlanPrune_KeepMath(t *testing.T) {
	cases := []struct {
		keep       int
		wantDelete []string
	}{
		{0, []string{"1", "2", "3", "4"}},
		{1, []string{"1", "2", "3"}},
		{3, []string{"1"}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.keep), func(t *testing.T) {
			p, err := PlanPrune(five(), blr, c.keep, "")
			if err != nil {
				t.Fatal(err)
			}
			got := IDs(p.Delete)
			slices.Sort(got)
			if !slices.Equal(got, c.wantDelete) {
				t.Errorf("delete = %v, want %v", got, c.wantDelete)
			}
			if len(p.Keep) != 1+c.keep {
				t.Errorf("keep = %v, want %d images", IDs(p.Keep), 1+c.keep)
			}
		})
	}
}

func TestPlanPrune_FewerThanKeepDeletesNothing(t *testing.T) {
	p, err := PlanPrune(five()[:2], blr, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Delete) != 0 {
		t.Errorf("delete = %v, want none", IDs(p.Delete))
	}
}

func TestPlanPrune_EqualCreatedDeletesLowerIDFirst(t *testing.T) {
	imgs := []provider.Image{snap("30", t0), snap("10", t0), snap("20", t0)}
	p, err := PlanPrune(imgs, blr, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := IDs(p.Keep); !slices.Equal(got, []string{"30"}) {
		t.Errorf("keep = %v, want [30]", got)
	}
	if got := IDs(p.Delete); !slices.Equal(got, []string{"10", "20"}) {
		t.Errorf("delete = %v, want [10 20] (oldest first)", got)
	}
}

func TestPlanPrune_ScopeFiltering(t *testing.T) {
	newest := snap("9", t0.Add(100*time.Hour))
	otherTag := with(snap("2", t0), func(i *provider.Image) {
		i.Tags = SnapshotTags("other", "x86_64", "blr1", "fish")
	})
	otherArch := with(snap("3", t0), func(i *provider.Image) {
		i.Tags = SnapshotTags("my-dev", "arm64", "blr1", "fish")
	})
	otherRegion := with(snap("4", t0), func(i *provider.Image) {
		i.Tags = SnapshotTags("my-dev", "x86_64", "nyc3", "fish")
		i.Regions = []string{"nyc3"}
	})
	inScope := snap("5", t0)
	p, err := PlanPrune([]provider.Image{newest, otherTag, otherArch, otherRegion, inScope}, blr, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := IDs(p.Delete); !slices.Equal(got, []string{"5"}) {
		t.Errorf("delete = %v, want only the in-scope [5]", got)
	}
}

func TestPlanPrune_NeverDeletesForeignOrPending(t *testing.T) {
	// A foreign image shares the user tag but has none of the markers;
	// a pending one is still being built. Neither is a candidate, and
	// neither counts toward the retained set.
	foreign := provider.Image{ID: "1", Created: t0, Regions: []string{"blr1"},
		Status: "available", Type: "snapshot", Tags: []string{"my-dev"}}
	pending := with(snap("2", t0), func(i *provider.Image) { i.Status = "pending" })
	imgs := append([]provider.Image{foreign, pending}, five()[2:]...)
	p, err := PlanPrune(imgs, blr, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := IDs(p.Delete); !slices.Equal(got, []string{"3", "4"}) {
		t.Errorf("delete = %v, want [3 4]", got)
	}
}

func TestPlanPrune_ProtectedNeverDeleted(t *testing.T) {
	p, err := PlanPrune(five(), blr, 0, "1")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(IDs(p.Delete), "1") {
		t.Errorf("protected image 1 in delete set %v", IDs(p.Delete))
	}
	if !slices.Contains(IDs(p.Keep), "1") {
		t.Errorf("protected image 1 missing from keep set %v", IDs(p.Keep))
	}
}

func TestPlanPrune_NegativeKeepIsError(t *testing.T) {
	if _, err := PlanPrune(five(), blr, -1, ""); err == nil {
		t.Fatal("want error for negative keepPrevious")
	}
}

func TestPlanPrune_IncompleteScopeIsError(t *testing.T) {
	for name, s := range map[string]Scope{
		"no tag":    {Arch: blr.Arch, Region: blr.Region},
		"no arch":   {Tag: blr.Tag, Region: blr.Region},
		"no region": {Tag: blr.Tag, Arch: blr.Arch},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := PlanPrune(five(), s, 0, "")
			if err == nil {
				t.Fatalf("want error, got plan deleting %v", IDs(p.Delete))
			}
		})
	}
}

func TestGroupScopes(t *testing.T) {
	nyc := with(snap("7", t0), func(i *provider.Image) {
		i.Tags = SnapshotTags("my-dev", "x86_64", "nyc3", "fish")
		i.Regions = []string{"nyc3"}
	})
	foreign := provider.Image{ID: "8", Status: "available", Type: "snapshot",
		Regions: []string{"sfo3"}, Tags: []string{"my-dev", "bivouac-region-sfo3"}}
	imgs := append(five(), nyc, foreign)

	got := GroupScopes(imgs, "my-dev", "", "")
	want := []Scope{blr, {Tag: "my-dev", Arch: "x86_64", Region: "nyc3"}}
	if !slices.Equal(got, want) {
		t.Errorf("GroupScopes = %v, want %v", got, want)
	}
	if got := GroupScopes(imgs, "my-dev", "nyc3", ""); !slices.Equal(got, want[1:]) {
		t.Errorf("region-filtered = %v, want %v", got, want[1:])
	}
	if got := GroupScopes(imgs, "my-dev", "", "arm64"); len(got) != 0 {
		t.Errorf("arch-filtered = %v, want none", got)
	}
}

func planFive(t *testing.T, f *fakeImages) PrunePlan {
	t.Helper()
	p, err := PlanPrune(f.images, blr, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.count("DeleteImage") != 0 {
		t.Fatalf("planning made delete calls: %v", f.calls)
	}
	return p
}

func TestExecutePrune_DeletesOldestFirst(t *testing.T) {
	f := newFake(five()...)
	r := ExecutePrune(context.Background(), f, planFive(t, f))
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if !slices.Equal(r.Deleted, []string{"1", "2", "3"}) || len(r.Pending) != 0 {
		t.Errorf("deleted %v pending %v, want [1 2 3] []", r.Deleted, r.Pending)
	}
	if got := IDs(f.images); !slices.Equal(got, []string{"4", "5"}) {
		t.Errorf("remaining %v, want retained [4 5] intact", got)
	}
}

func TestExecutePrune_RecheckStopsOnChange(t *testing.T) {
	cases := map[string]func(*provider.Image){
		"tag removed":  func(i *provider.Image) { i.Tags = without(i.Tags, "my-dev") },
		"not eligible": func(i *provider.Image) { i.Status = "deleted" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(five()...)
			p := planFive(t, f)
			f.onGet = func(f *fakeImages, id string) {
				if id == "2" {
					for i := range f.images {
						if f.images[i].ID == "2" {
							change(&f.images[i])
						}
					}
				}
			}
			r := ExecutePrune(context.Background(), f, p)
			if r.Err == nil {
				t.Fatal("want an error reporting the concurrent change")
			}
			if !slices.Equal(r.Deleted, []string{"1"}) || !slices.Equal(r.Pending, []string{"2", "3"}) {
				t.Errorf("deleted %v pending %v, want [1] [2 3]", r.Deleted, r.Pending)
			}
			if f.count("DeleteImage") != 1 {
				t.Errorf("delete calls %v, want only image 1", f.calls)
			}
		})
	}
}

func TestExecutePrune_MissingIsIdempotent(t *testing.T) {
	f := newFake(five()...)
	p := planFive(t, f)
	f.images = slices.DeleteFunc(f.images, func(i provider.Image) bool { return i.ID == "2" })
	r := ExecutePrune(context.Background(), f, p)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if !slices.Equal(r.Deleted, []string{"1", "2", "3"}) || len(r.Pending) != 0 {
		t.Errorf("deleted %v pending %v, want [1 2 3] []", r.Deleted, r.Pending)
	}
}

func TestExecutePrune_DeleteRaceNotFoundIsIdempotent(t *testing.T) {
	f := newFake(five()...)
	p := planFive(t, f)
	f.deleteErr = map[string]error{"2": fmt.Errorf("image 2: %w", provider.ErrImageNotFound)}
	r := ExecutePrune(context.Background(), f, p)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if !slices.Equal(r.Deleted, []string{"1", "2", "3"}) {
		t.Errorf("deleted %v, want [1 2 3]", r.Deleted)
	}
}

func TestExecutePrune_DeleteFailureReportsPartial(t *testing.T) {
	for _, code := range []int{403, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newFake(five()...)
			p := planFive(t, f)
			apiErr := fmt.Errorf("DELETE image 2: %d", code)
			f.deleteErr = map[string]error{"2": apiErr}
			r := ExecutePrune(context.Background(), f, p)
			if !errors.Is(r.Err, apiErr) {
				t.Fatalf("err = %v, want it to wrap %v", r.Err, apiErr)
			}
			if !slices.Equal(r.Deleted, []string{"1"}) || !slices.Equal(r.Pending, []string{"2", "3"}) {
				t.Errorf("deleted %v pending %v, want [1] [2 3]", r.Deleted, r.Pending)
			}
			if got := IDs(f.images); !slices.Equal(got, []string{"2", "3", "4", "5"}) {
				t.Errorf("remaining %v, want [2 3 4 5]", got)
			}
		})
	}
}

func TestPruneResult_Summary(t *testing.T) {
	r := PruneResult{Deleted: []string{"1"}, Pending: []string{"2", "3"}}
	if got, want := r.Summary(), "deleted 1, pending 2"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// IDs feeds --json output directly, where an empty list must encode as
// [] rather than null.
func TestIDs_NeverNil(t *testing.T) {
	if got := IDs(nil); got == nil || len(got) != 0 {
		t.Errorf("IDs(nil) = %#v, want an empty non-nil slice", got)
	}
	if got := IDs([]provider.Image{{ID: "7"}, {ID: "9"}}); !slices.Equal(got, []string{"7", "9"}) {
		t.Errorf("IDs = %v, want [7 9]", got)
	}
}
