package cmd

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// pruneFixture holds three my-dev builds in nyc3 and two in blr1, plus
// a foreign image sharing the tag that must never be touched.
func pruneFixture(t *testing.T) *imageFake {
	return newImageFake(t,
		snapIn("1", "nyc3", 1), snapIn("2", "nyc3", 2), snapIn("3", "nyc3", 3),
		snapIn("11", "blr1", 1), snapIn("12", "blr1", 2),
		foreignSnap("40"),
	)
}

func execPrune(t *testing.T, p *imageFake, interactive bool, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newImagePruneCmdWithProvider(fakeResolve(p), func() bool { return interactive })
	c.SetIn(strings.NewReader(stdin))
	return execImageSub(t, c, args...)
}

func TestImagePrune_DryRunDeletesNothing(t *testing.T) {
	p := pruneFixture(t)
	stdout, _, err := execPrune(t, p, false, "", "--tag", "my-dev", "--keep-previous", "0", "--dry-run")
	if err != nil {
		t.Fatalf("image prune --dry-run: %v", err)
	}
	if len(p.deleted) != 0 {
		t.Errorf("deleted %v on a dry run", p.deleted)
	}
	for _, id := range []string{"1", "2", "11"} {
		if !strings.Contains(stdout, id) {
			t.Errorf("preview missing %s:\n%s", id, stdout)
		}
	}
}

func TestImagePrune_NoTerminalNeedsYes(t *testing.T) {
	p := pruneFixture(t)
	stdout, stderr, err := execPrune(t, p, false, "y\n", "--tag", "my-dev", "--keep-previous", "0")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want one naming --yes", err)
	}
	if strings.Contains(stdout+stderr, "[y/N]") {
		t.Errorf("prompted without a terminal:\n%s%s", stdout, stderr)
	}
	if len(p.deleted) != 0 {
		t.Errorf("deleted %v without confirmation", p.deleted)
	}
}

func TestImagePrune_ConfirmsInATerminal(t *testing.T) {
	p := pruneFixture(t)
	if _, _, err := execPrune(t, p, true, "n\n", "--tag", "my-dev", "--keep-previous", "0"); err != nil {
		t.Fatalf("declined prune: %v", err)
	}
	if len(p.deleted) != 0 {
		t.Errorf("deleted %v after declining", p.deleted)
	}
	if _, _, err := execPrune(t, p, true, "y\n", "--tag", "my-dev", "--keep-previous", "0"); err != nil {
		t.Fatalf("confirmed prune: %v", err)
	}
	if len(p.deleted) != 3 {
		t.Errorf("deleted %v after confirming, want 3", p.deleted)
	}
}

func TestImagePrune_YesGroupsByRegionAndArch(t *testing.T) {
	p := pruneFixture(t)
	if _, _, err := execPrune(t, p, false, "", "--tag", "my-dev", "--keep-previous", "0", "--yes"); err != nil {
		t.Fatalf("image prune --yes: %v", err)
	}
	// Per group: nyc3 keeps 3 and drops 1, 2; blr1 keeps 12 and drops
	// 11. Pooled, blr1's builds would have been compared against nyc3's.
	got := slices.Clone(p.deleted)
	slices.Sort(got)
	if !slices.Equal(got, []string{"1", "11", "2"}) {
		t.Errorf("deleted = %v, want 1, 2 and 11", got)
	}
}

func TestImagePrune_RegionFilter(t *testing.T) {
	p := pruneFixture(t)
	if _, _, err := execPrune(t, p, false, "", "--tag", "my-dev", "--keep-previous", "0", "--yes", "--region", "blr1"); err != nil {
		t.Fatalf("image prune: %v", err)
	}
	if !slices.Equal(p.deleted, []string{"11"}) {
		t.Errorf("deleted = %v, want only 11", p.deleted)
	}
}

func TestImagePrune_ContradictoryFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"dry-run with yes": {"--tag", "my-dev", "--dry-run", "--yes"},
		"negative keep":    {"--tag", "my-dev", "--keep-previous", "-1", "--yes"},
		"invalid tag":      {"--tag", "bad tag!", "--yes"},
		"repo":             {"--tag", "my-dev", "--yes", "--repo", "."},
		"no tag":           {"--yes"},
	} {
		t.Run(name, func(t *testing.T) {
			p := pruneFixture(t)
			if _, _, err := execPrune(t, p, false, "", args...); err == nil {
				t.Fatal("image prune succeeded, want a flag error")
			}
			if len(p.deleted) != 0 || len(p.listTags) != 0 {
				t.Errorf("provider touched: deleted %v, listed %v", p.deleted, p.listTags)
			}
		})
	}
}

func TestImagePrune_PartialFailureJSON(t *testing.T) {
	p := pruneFixture(t)
	p.deleteErr = map[string]error{"2": errors.New("403 forbidden")}
	stdout, _, err := execPrune(t, p, false, "", "--tag", "my-dev", "--keep-previous", "0", "--yes", "--json")
	if err == nil {
		t.Fatal("image prune succeeded despite a failed delete")
	}
	var got struct {
		Error        string   `json:"error"`
		Phase        string   `json:"phase"`
		ImageCreated *bool    `json:"image_created"`
		Cleanup      string   `json:"cleanup"`
		Deleted      []string `json:"deleted"`
		Pending      []string `json:"pending"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if got.Phase != "prune" || got.ImageCreated == nil || *got.ImageCreated || got.Error == "" || got.Cleanup == "" {
		t.Errorf("error fields = %+v", got)
	}
	// blr1 sorts first, so 11 went; nyc3 deleted 1 then failed on 2.
	if !slices.Equal(got.Deleted, []string{"11", "1"}) || !slices.Equal(got.Pending, []string{"2"}) {
		t.Errorf("deleted %v pending %v, want [11 1] and [2]", got.Deleted, got.Pending)
	}
}

func TestImagePrune_HelpListsFlags(t *testing.T) {
	stdout, _, err := runImageCmd(t, "image", "prune", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--keep-previous", "--dry-run", "--yes", "--region", "--arch"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("help missing %s", flag)
		}
	}
}
