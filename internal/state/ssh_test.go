package state

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshidentity"
)

func TestSSHMetadataOptionalAndRoundTrips(t *testing.T) {
	var old Record
	if err := json.Unmarshal([]byte(`{"name":"legacy","ip":"192.0.2.1"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.SSH != nil {
		t.Fatal("legacy acquired identity")
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"ssh"`) {
		t.Fatal("nil metadata serialized")
	}
	old.SSH = &sshidentity.Connection{Name: "legacy", Alias: "bivouac-legacy", User: "me", Keys: []sshidentity.Identity{{PublicPath: "/keys/custom.pub", PrivatePath: "/keys/custom", Fingerprint: "SHA256:identity"}}}
	raw, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SSH == nil || got.SSH.Keys[0].PublicPath != "/keys/custom.pub" {
		t.Fatal("metadata lost")
	}
}
