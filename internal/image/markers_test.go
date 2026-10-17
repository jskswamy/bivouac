package image

import (
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotTags(t *testing.T) {
	got := SnapshotTags("my-dev", "x86_64", "blr1", "fish")
	want := []string{
		"bivouac-image", "bivouac-format-1", "bivouac-arch-x86_64",
		"bivouac-region-blr1", "bivouac-shell-fish", "my-dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SnapshotTags = %v, want %v", got, want)
	}
}

func TestParseMarkers_RoundTrip(t *testing.T) {
	m, ok := ParseMarkers(SnapshotTags("my-dev", "x86_64", "blr1", "fish"))
	if !ok {
		t.Fatal("ParseMarkers rejected a complete marker set")
	}
	want := Markers{Arch: "x86_64", Region: "blr1", Shell: "fish", Format: 1}
	if m != want {
		t.Fatalf("ParseMarkers = %+v, want %+v", m, want)
	}
}

func TestParseMarkers_RejectsMissing(t *testing.T) {
	full := SnapshotTags("my-dev", "x86_64", "blr1", "fish")
	for i := 0; i < len(full)-1; i++ { // the last entry is the user tag
		tags := append(append([]string(nil), full[:i]...), full[i+1:]...)
		if _, ok := ParseMarkers(tags); ok {
			t.Errorf("ParseMarkers accepted a set missing %q", full[i])
		}
	}
}

func TestParseMarkers_RejectsDuplicated(t *testing.T) {
	for _, extra := range []string{
		"bivouac-image", "bivouac-format-2", "bivouac-arch-arm64",
		"bivouac-region-nyc1", "bivouac-shell-zsh",
	} {
		tags := append(SnapshotTags("my-dev", "x86_64", "blr1", "fish"), extra)
		if _, ok := ParseMarkers(tags); ok {
			t.Errorf("ParseMarkers accepted a duplicated marker %q", extra)
		}
	}
}

func TestParseMarkers_RejectsBadFormat(t *testing.T) {
	tags := []string{"bivouac-image", "bivouac-format-x", "bivouac-arch-x86_64", "bivouac-region-blr1", "bivouac-shell-fish"}
	if _, ok := ParseMarkers(tags); ok {
		t.Fatal("ParseMarkers accepted a non-numeric format")
	}
}

func TestValidUserTag(t *testing.T) {
	for _, ok := range []string{"my-dev", "team:dev_1", strings.Repeat("a", 255)} {
		if err := ValidUserTag(ok); err != nil {
			t.Errorf("ValidUserTag(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "bivouac-x", "my dev", "a/b", strings.Repeat("a", 256)} {
		if err := ValidUserTag(bad); err == nil {
			t.Errorf("ValidUserTag(%q) = nil, want error", bad)
		}
	}
}
