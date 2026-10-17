// Package image owns Bivouac's user-built cloud images: the provider
// tags that mark a snapshot as ours, and turning a config image
// reference into the concrete image a launch uses.
package image

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DigitalOcean tags are the only metadata an image carries that the API
// returns, so ownership and compatibility live in them. A user tag alone
// is not enough: it may match unrelated images or Droplets in the
// account, and only the full marker set makes a snapshot ours.
const (
	MarkerManaged  = "bivouac-image"
	FormatVersion  = 1
	StockSourceX86 = "ubuntu-24-04-x64"
)

const (
	markerPrefix = "bivouac-"
	prefixFormat = markerPrefix + "format-"
	prefixArch   = markerPrefix + "arch-"
	prefixRegion = markerPrefix + "region-"
	prefixShell  = markerPrefix + "shell-"
)

func ArchTag(arch string) string     { return prefixArch + arch }
func RegionTag(region string) string { return prefixRegion + region }
func ShellTag(shell string) string   { return prefixShell + shell }
func FormatTag() string              { return prefixFormat + strconv.Itoa(FormatVersion) }

// SnapshotTags is the complete marker set plus the user tag, in this order.
func SnapshotTags(userTag, arch, region, shell string) []string {
	return []string{MarkerManaged, FormatTag(), ArchTag(arch), RegionTag(region), ShellTag(shell), userTag}
}

// Markers is what an image's tags say about how it was built.
type Markers struct {
	Arch, Region, Shell string
	Format              int
}

// ParseMarkers succeeds only when every marker is present exactly once.
// A duplicate (two arch tags, say) is rejected rather than resolved by
// picking one, since it means someone tagged the image by hand and the
// build that produced it is no longer what the tags describe.
func ParseMarkers(tags []string) (Markers, bool) {
	var m Markers
	seen := map[string]int{}
	for _, t := range tags {
		switch {
		case t == MarkerManaged:
			seen[MarkerManaged]++
		case strings.HasPrefix(t, prefixFormat):
			n, err := strconv.Atoi(strings.TrimPrefix(t, prefixFormat))
			if err != nil {
				return Markers{}, false
			}
			m.Format = n
			seen[prefixFormat]++
		case strings.HasPrefix(t, prefixArch):
			m.Arch = strings.TrimPrefix(t, prefixArch)
			seen[prefixArch]++
		case strings.HasPrefix(t, prefixRegion):
			m.Region = strings.TrimPrefix(t, prefixRegion)
			seen[prefixRegion]++
		case strings.HasPrefix(t, prefixShell):
			m.Shell = strings.TrimPrefix(t, prefixShell)
			seen[prefixShell]++
		}
	}
	for _, k := range []string{MarkerManaged, prefixFormat, prefixArch, prefixRegion, prefixShell} {
		if seen[k] != 1 {
			return Markers{}, false
		}
	}
	if m.Arch == "" || m.Region == "" || m.Shell == "" {
		return Markers{}, false
	}
	return m, true
}

// Scope narrows images to one build target. An empty Tag matches any
// user tag, which is how a numeric ID is validated.
type Scope struct{ Tag, Arch, Region string }

var userTagChars = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)

// ValidUserTag checks a tag against DigitalOcean's tag rules, and keeps
// it out of the marker namespace so a user tag can never forge or
// shadow a marker.
func ValidUserTag(tag string) error {
	switch {
	case tag == "":
		return fmt.Errorf("image tag is empty")
	case len(tag) > 255:
		return fmt.Errorf("image tag %q is longer than 255 characters", tag)
	case !userTagChars.MatchString(tag):
		return fmt.Errorf("image tag %q may only contain letters, digits, ':', '_' and '-'", tag)
	case strings.HasPrefix(tag, markerPrefix):
		return fmt.Errorf("image tag %q uses the reserved %q prefix", tag, markerPrefix)
	}
	return nil
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
