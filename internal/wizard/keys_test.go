package wizard

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/sshkeys"
)

func agentKey(fp, comment string) sshkeys.Key {
	return sshkeys.Key{Fingerprint: fp, Comment: comment, Source: sshkeys.Agent, PublicKey: "ssh-ed25519 AAAA " + comment}
}

func diskKey(fp, comment string) sshkeys.Key {
	return sshkeys.Key{Fingerprint: fp, Comment: comment, Source: sshkeys.Disk, PublicKey: "ssh-ed25519 BBBB " + comment}
}

func labels(o KeyOffer) []string {
	out := make([]string, 0, len(o.Choices))
	for _, c := range o.Choices {
		out = append(out, c.Label)
	}
	return out
}

func selected(o KeyOffer) []string {
	var out []string
	for _, c := range o.Choices {
		if c.Selected {
			out = append(out, c.Label)
		}
	}
	return out
}

func find(t *testing.T, o KeyOffer, label string) KeyChoice {
	t.Helper()
	for _, c := range o.Choices {
		if c.Label == label {
			return c
		}
	}
	t.Fatalf("no choice labelled %q in %v", label, labels(o))
	return KeyChoice{}
}

// Tier 1: no account consulted. Every local key is offered, and nothing
// claims to know whether it is registered.
func TestOfferKeys_Tier1_LocalOnly(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{agentKey("aa", "laptop"), diskKey("bb", "old")}, nil, false, nil)

	if o.AccountKnown {
		t.Error("AccountKnown = true, want false with no account lookup")
	}
	if got := labels(o); !reflect.DeepEqual(got, []string{"laptop", "old"}) {
		t.Errorf("labels = %v, want the local keys in discovery order", got)
	}
	for _, c := range o.Choices {
		if strings.Contains(c.Detail, "registered") {
			t.Errorf("%q claims %q without an account lookup", c.Label, c.Detail)
		}
	}
}

// With no way to tell registered from not, the agent keys are the ones
// preselected: a key the agent holds is one in active use.
func TestOfferKeys_Tier1_PreselectsAgentKeys(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{agentKey("aa", "laptop"), diskKey("bb", "old")}, nil, false, nil)
	if got := selected(o); !reflect.DeepEqual(got, []string{"laptop"}) {
		t.Errorf("selected = %v, want just the agent key", got)
	}
}

func TestOfferKeys_Tier1_NoAgentKeysSelectsNothing(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{diskKey("bb", "old")}, nil, false, nil)
	if got := selected(o); len(got) != 0 {
		t.Errorf("selected = %v, want nothing so the user chooses", got)
	}
}

// Tier 2: the account is known, so each local key can say whether it is
// there, and keys that are certain to work are the default.
func TestOfferKeys_Tier2_MarksRegistrationAndPreselectsWhatWorks(t *testing.T) {
	local := []sshkeys.Key{agentKey("aa", "laptop"), diskKey("bb", "old")}
	account := []provider.SSHKey{{Fingerprint: "aa", Name: "laptop"}}

	o := OfferKeys(local, account, true, nil)
	if !o.AccountKnown {
		t.Error("AccountKnown = false, want true")
	}
	if c := find(t, o, "laptop"); !c.Registered || !c.Selected {
		t.Errorf("laptop = %+v, want registered and selected", c)
	}
	if c := find(t, o, "old"); c.Registered || c.Selected {
		t.Errorf("old = %+v, want unregistered and unselected", c)
	}
	if c := find(t, o, "old"); !strings.Contains(c.Detail, "not on your account") {
		t.Errorf("old detail = %q, want it to say it is not on the account", c.Detail)
	}
}

// Account keys with no local counterpart are shown so the second-machine
// case is reachable, labelled so it is obvious, and never preselected --
// picking one is how you get a droplet you cannot log into.
func TestOfferKeys_Tier2_AccountOnlyKeysShownLabelledUnselected(t *testing.T) {
	local := []sshkeys.Key{agentKey("aa", "laptop")}
	account := []provider.SSHKey{{Fingerprint: "aa", Name: "laptop"}, {Fingerprint: "zz", Name: "work-laptop"}}

	o := OfferKeys(local, account, true, nil)
	if got := labels(o); !reflect.DeepEqual(got, []string{"laptop", "work-laptop"}) {
		t.Errorf("labels = %v, want local first then account-only", got)
	}
	c := find(t, o, "work-laptop")
	if c.Local {
		t.Error("work-laptop marked Local")
	}
	if c.Selected {
		t.Error("work-laptop preselected; picking one must be deliberate")
	}
	if !strings.Contains(c.Detail, "not on this machine") {
		t.Errorf("detail = %q, want it to say the key is not held here", c.Detail)
	}
}

// Editing an existing config: whatever the file already names stays
// selected, wherever it came from.
func TestOfferKeys_CurrentValuesStaySelected(t *testing.T) {
	local := []sshkeys.Key{agentKey("aa", "laptop"), diskKey("bb", "old")}
	account := []provider.SSHKey{{Fingerprint: "zz", Name: "work-laptop"}}

	o := OfferKeys(local, account, true, []string{"bb", "zz"})
	got := selected(o)
	for _, want := range []string{"old", "work-laptop"} {
		if !containsString(got, want) {
			t.Errorf("selected = %v, want it to include %q from the existing config", got, want)
		}
	}
}

// A fingerprint in the config that matches nothing anywhere must survive
// the round trip, or editing one setting silently drops a key.
func TestOfferKeys_UnknownCurrentValueIsKept(t *testing.T) {
	o := OfferKeys(nil, nil, false, []string{"ff:ee:dd"})
	c := find(t, o, "ff:ee:dd")
	if !c.Selected {
		t.Error("an existing fingerprint was not kept selected")
	}
	if !strings.Contains(c.Detail, "from your config") {
		t.Errorf("detail = %q, want it to say where the value came from", c.Detail)
	}
}

// A smartcard's comment names the card, not the slot: two SSH-capable
// keys on the same card report the same comment. Left alone, that makes
// two choices in the picker impossible to tell apart -- and impossible
// to tell which one was actually picked afterward.
func TestOfferKeys_SharedCommentIsDisambiguatedByFingerprint(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{
		agentKey("05:55:6c:fb:81:45:3d:3e:f2:4f:e7:a0:16:6e:2d:d1", "cardno:26_969_765"),
		agentKey("44:06:b5:b5:2a:17:1b:e1:6b:ec:51:4c:f7:86:ce:17", "cardno:26_969_765"),
	}, nil, false, nil)

	want := []string{
		"cardno:26_969_765 (2d:d1)",
		"cardno:26_969_765 (ce:17)",
	}
	if got := labels(o); !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
}

// A comment only one key holds needs no disambiguation -- appending a
// fingerprint suffix to every label regardless would make the common
// case noisier for the sake of the rare one.
func TestOfferKeys_UniqueCommentIsLeftAlone(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{agentKey("aa", "laptop")}, nil, false, nil)
	if got := labels(o); !reflect.DeepEqual(got, []string{"laptop"}) {
		t.Errorf("labels = %v, want the comment unchanged", got)
	}
}

func TestOfferKeys_NothingAnywhere(t *testing.T) {
	o := OfferKeys(nil, nil, false, nil)
	if len(o.Choices) != 0 {
		t.Errorf("Choices = %v, want none", o.Choices)
	}
}

// Only keys the user actually chose, that they hold, and that are not on
// the account, are upload candidates.
func TestNeedsUpload(t *testing.T) {
	local := []sshkeys.Key{agentKey("aa", "laptop"), diskKey("bb", "old"), diskKey("cc", "spare")}
	account := []provider.SSHKey{{Fingerprint: "aa", Name: "laptop"}, {Fingerprint: "zz", Name: "work"}}
	o := OfferKeys(local, account, true, nil)

	got := NeedsUpload(o, []string{"aa", "bb", "zz"})
	if len(got) != 1 {
		t.Fatalf("NeedsUpload = %+v, want just the unregistered local key", got)
	}
	if got[0].Label != "old" {
		t.Errorf("candidate = %q, want old", got[0].Label)
	}
	if got[0].PublicKey == "" {
		t.Error("candidate carries no public key, so nothing could be uploaded")
	}
	if got[0].SuggestedName != "old" {
		t.Errorf("SuggestedName = %q, want the key's comment", got[0].SuggestedName)
	}
}

// Without an account lookup there is nothing to compare against, so
// nothing can be known to need uploading.
func TestNeedsUpload_NothingWhenTheAccountIsUnknown(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{diskKey("bb", "old")}, nil, false, nil)
	if got := NeedsUpload(o, []string{"bb"}); len(got) != 0 {
		t.Errorf("NeedsUpload = %+v, want none without an account lookup", got)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// A fingerprint typed by hand never appears among the choices, so the
// upload check cannot see it. The account check still must: writing one
// the account lacks makes every later `up` fail at create.
func TestUnregistered_CatchesTypedFingerprints(t *testing.T) {
	local := []sshkeys.Key{agentKey("aa", "laptop")}
	account := []provider.SSHKey{{Fingerprint: "aa", Name: "laptop"}}
	o := OfferKeys(local, account, true, nil)

	got := o.Unregistered([]string{"aa", "typed:by:hand"})
	if !reflect.DeepEqual(got, []string{"typed:by:hand"}) {
		t.Errorf("Unregistered = %v, want just the typed fingerprint", got)
	}
}

// A key carried in from the existing config is a choice, but not a local
// one, so nothing can upload it. It still deserves to be named.
func TestUnregistered_CatchesConfigFingerprints(t *testing.T) {
	o := OfferKeys(nil, []provider.SSHKey{{Fingerprint: "aa", Name: "laptop"}}, true, []string{"ff:ee"})

	got := o.Unregistered([]string{"ff:ee"})
	if !reflect.DeepEqual(got, []string{"ff:ee"}) {
		t.Errorf("Unregistered = %v, want the config's own fingerprint", got)
	}
}

func TestUnregistered_IgnoresRegisteredKeys(t *testing.T) {
	o := OfferKeys(nil, []provider.SSHKey{{Fingerprint: "aa"}, {Fingerprint: "zz"}}, true, nil)
	if got := o.Unregistered([]string{"aa", "zz"}); len(got) != 0 {
		t.Errorf("Unregistered = %v, want none", got)
	}
}

// Without an account lookup nothing is known to be missing, so nothing
// may be claimed.
func TestUnregistered_SilentWhenTheAccountIsUnknown(t *testing.T) {
	o := OfferKeys([]sshkeys.Key{agentKey("aa", "laptop")}, nil, false, nil)
	if got := o.Unregistered([]string{"aa", "anything"}); len(got) != 0 {
		t.Errorf("Unregistered = %v, want none without an account lookup", got)
	}
}

func TestUnregistered_ZeroOfferSaysNothing(t *testing.T) {
	var o KeyOffer
	if got := o.Unregistered([]string{"aa"}); len(got) != 0 {
		t.Errorf("Unregistered = %v, want none", got)
	}
}
