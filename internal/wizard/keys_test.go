package wizard

import (
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshkeys"
)

func TestOfferKeysPathsOnly(t *testing.T) {
	local := []sshkeys.Key{
		{Path: "/keys/selected.pub", Fingerprint: "selected", Comment: "laptop", Source: sshkeys.Agent},
		{Path: "/keys/other.pub", Fingerprint: "other", Comment: "old", Source: sshkeys.Disk},
		{Fingerprint: "hardware", Comment: "hardware export needed", Source: sshkeys.Agent},
	}
	account := []provider.SSHKey{{Fingerprint: "selected"}, {Fingerprint: "account-only", Name: "colleague"}}
	offer := OfferKeys(local, account, true, []string{"/keys/other.pub", "12345"})
	if len(offer.Choices) != 2 {
		t.Fatalf("offered non-file keys: %+v", offer.Choices)
	}
	for _, c := range offer.Choices {
		if c.Value == "" || !strings.HasSuffix(c.Value, ".pub") {
			t.Fatalf("value=%q", c.Value)
		}
		if c.Value == "/keys/selected.pub" && (!c.Registered || !c.Selected) {
			t.Fatalf("registered choice=%+v", c)
		}
		if c.Value == "/keys/other.pub" && (!c.Selected || c.Registered) {
			t.Fatalf("current choice=%+v", c)
		}
		if !strings.Contains(c.Label, ".pub") || !strings.HasPrefix(c.SuggestedName, "bivouac-") {
			t.Fatalf("choice=%+v", c)
		}
	}
	missing := NeedsUpload(offer, []string{"/keys/other.pub"})
	if len(missing) != 1 || missing[0].Value != "/keys/other.pub" {
		t.Fatalf("pending=%+v", missing)
	}
}

func TestOfferKeysCredentialFreeDefaults(t *testing.T) {
	offer := OfferKeys([]sshkeys.Key{{Path: "/keys/card.pub", Source: sshkeys.Agent}, {Path: "/keys/disk.pub", Source: sshkeys.Disk}}, nil, false, nil)
	if offer.AccountKnown || !offer.Choices[0].Selected || offer.Choices[1].Selected {
		t.Fatalf("offer=%+v", offer)
	}
	if len(NeedsUpload(offer, []string{"/keys/card.pub"})) != 0 {
		t.Fatal("claimed missing without account lookup")
	}
}
