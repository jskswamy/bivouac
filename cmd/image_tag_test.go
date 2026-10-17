package cmd

import (
	"encoding/json"
	"testing"
)

func TestImageTag_RejectsIncompatibleImages(t *testing.T) {
	notReady := snapIn("30", "nyc3", 5)
	notReady.Status = "pending"
	for name, args := range map[string][]string{
		"foreign":     {"40", "other"},
		"not ready":   {"30", "other"},
		"missing":     {"99", "other"},
		"invalid tag": {"10", "bad tag!"},
		"marker tag":  {"10", "bivouac-image"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newImageFake(t, snapIn("10", "nyc3", 1), notReady, foreignSnap("40"))
			if _, _, err := execImageSub(t, newImageTagCmdWithProvider(fakeResolve(p)), args...); err == nil {
				t.Fatal("image tag succeeded, want an error")
			}
			if len(p.tagged) != 0 {
				t.Errorf("TagImage called %v, want never", p.tagged)
			}
		})
	}
}

func TestImageTag_TagsCompatibleImage(t *testing.T) {
	p := newImageFake(t, snapIn("10", "nyc3", 1))
	stdout, _, err := execImageSub(t, newImageTagCmdWithProvider(fakeResolve(p)), "10", "stable", "--json")
	if err != nil {
		t.Fatalf("image tag: %v", err)
	}
	if len(p.tagged) != 1 || p.tagged[0] != [2]string{"10", "stable"} {
		t.Errorf("TagImage calls = %v, want one for 10/stable", p.tagged)
	}
	var got struct {
		ImageID   string `json:"image_id"`
		Tag       string `json:"tag"`
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if got.ImageID != "10" || got.Tag != "stable" || got.Reference != "tag:stable" {
		t.Errorf("result = %+v", got)
	}
}
