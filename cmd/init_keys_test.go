package cmd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/sshkeys"
)

// fakeRegistry stands in for a provider's SSH key API.
type fakeRegistry struct {
	keys []provider.SSHKey
	// listErr and createErr fail the respective calls.
	listErr, createErr error
	// createdButListed mimics a create that fails while the key is in
	// fact on the account -- a race, or a key already there under
	// another name.
	createdButListed *provider.SSHKey
	created          []provider.SSHKey
}

func (f *fakeRegistry) ListKeys(ctx context.Context) ([]provider.SSHKey, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := append([]provider.SSHKey{}, f.keys...)
	if f.createdButListed != nil {
		out = append(out, *f.createdButListed)
	}
	return out, nil
}

func (f *fakeRegistry) CreateKey(ctx context.Context, name, publicKey string) (provider.SSHKey, error) {
	if f.createErr != nil {
		return provider.SSHKey{}, f.createErr
	}
	key := provider.SSHKey{ID: "new", Name: name, PublicKey: publicKey, Fingerprint: fingerprintOfPublicKey(publicKey)}
	f.created = append(f.created, key)
	f.keys = append(f.keys, key)
	return key, nil
}

// fingerprintOfPublicKey maps the fake public keys these tests use back
// to their fingerprints.
func fingerprintOfPublicKey(pub string) string {
	return strings.TrimPrefix(pub, "ssh-ed25519 AAAA ")
}

func localKey(fp, comment string, source sshkeys.Source) sshkeys.Key {
	return sshkeys.Key{Fingerprint: fp, Comment: comment, Source: source, PublicKey: "ssh-ed25519 AAAA " + fp}
}

// runWithKeys drives the flow with injected key sources.
func (f *initFixture) runWithKeys(t *testing.T, s *scriptedPrompter, local []sshkeys.Key, reg provider.KeyRegistry) error {
	t.Helper()
	sources := keySources{
		local: func() ([]sshkeys.Key, error) { return local, nil },
		registry: func(ctx context.Context) (provider.KeyRegistry, error) {
			if reg == nil {
				return nil, nil
			}
			return reg, nil
		},
	}
	return runInitFlowWith(context.Background(), f.out, s, f.root, sources)
}

func baseScript() *scriptedPrompter {
	s := newScript()
	s.answers = map[string]any{"region": "nyc3", "size": "s-1vcpu-1gb", "template": "python"}
	s.decisions = map[string]any{promptSavePreset: false}
	return s
}

// Tier 1: no registry. The keys this machine holds are offered, and the
// chosen fingerprints land in base.pkl.
func TestInitFlow_Keys_Tier1WritesChosenFingerprints(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"aa:bb"}

	local := []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent), localKey("cc:dd", "old", sshkeys.Disk)}
	if err := f.runWithKeys(t, s, local, nil); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if !s.keysAsked {
		t.Fatal("the SSH key question was never asked")
	}
	if s.keyOffer.AccountKnown {
		t.Error("AccountKnown = true with no registry")
	}
	got := f.values(t, f.basePath)
	if !reflect.DeepEqual(got["sshKeys"], []string{"aa:bb"}) {
		t.Errorf("sshKeys = %v, want the chosen fingerprint", got["sshKeys"])
	}
}

// The committed file never sees a key, wherever the answer came from.
func TestInitFlow_Keys_NeverReachTheProjectFile(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"aa:bb"}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent)}, nil); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if text := f.text(t, f.project); strings.Contains(text, "aa:bb") {
		t.Errorf("cloudlab.pkl holds the key:\n%s", text)
	}
}

// Tier 2: the registry is consulted, so the offer can say which keys are
// registered.
func TestInitFlow_Keys_Tier2MarksRegistration(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"aa:bb"}
	reg := &fakeRegistry{keys: []provider.SSHKey{{Fingerprint: "aa:bb", Name: "laptop"}}}

	local := []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent), localKey("cc:dd", "old", sshkeys.Disk)}
	if err := f.runWithKeys(t, s, local, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if !s.keyOffer.AccountKnown {
		t.Fatal("AccountKnown = false with a registry present")
	}
	for _, c := range s.keyOffer.Choices {
		if c.Fingerprint == "aa:bb" && !c.Registered {
			t.Error("the registered key was not marked registered")
		}
		if c.Fingerprint == "cc:dd" && c.Registered {
			t.Error("an unregistered key was marked registered")
		}
	}
}

// Tier 3: choosing a key the account lacks offers to upload it, and the
// upload actually happens on yes.
func TestInitFlow_Keys_UploadsOnConfirm(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"cc:dd"}
	s.decisions[promptUploadKey] = true
	s.decisions[promptUploadKeyName] = "old-desktop"
	reg := &fakeRegistry{}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("cc:dd", "old", sshkeys.Disk)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if len(reg.created) != 1 {
		t.Fatalf("created %d keys, want 1", len(reg.created))
	}
	if reg.created[0].Name != "old-desktop" {
		t.Errorf("registered name = %q, want old-desktop", reg.created[0].Name)
	}
	if got := f.values(t, f.basePath); !reflect.DeepEqual(got["sshKeys"], []string{"cc:dd"}) {
		t.Errorf("sshKeys = %v, want the uploaded key kept", got["sshKeys"])
	}
}

// Declining the upload drops the key rather than writing a fingerprint
// that is known to fail at create time.
func TestInitFlow_Keys_DecliningUploadDropsTheKey(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"cc:dd"}
	s.decisions[promptUploadKey] = false
	reg := &fakeRegistry{}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("cc:dd", "old", sshkeys.Disk)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if len(reg.created) != 0 {
		t.Error("a key was uploaded despite declining")
	}
	if got := f.values(t, f.basePath); got["sshKeys"] != nil {
		t.Errorf("sshKeys = %v, want the unregistered key left out", got["sshKeys"])
	}
	if !strings.Contains(f.out.String(), "no SSH keys") {
		t.Errorf("nothing warned that the config names no keys:\n%s", f.out)
	}
}

// A create can fail because the key is already there. What matters is
// that it ends up registered, not that the call succeeded.
func TestInitFlow_Keys_AlreadyRegisteredCountsAsUploaded(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"cc:dd"}
	s.decisions[promptUploadKey] = true
	s.decisions[promptUploadKeyName] = "old-desktop"
	reg := &fakeRegistry{
		createErr:        errors.New("422 SSH Key is already in use on your account"),
		createdButListed: &provider.SSHKey{Fingerprint: "cc:dd", Name: "already-there"},
	}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("cc:dd", "old", sshkeys.Disk)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if got := f.values(t, f.basePath); !reflect.DeepEqual(got["sshKeys"], []string{"cc:dd"}) {
		t.Errorf("sshKeys = %v, want the key kept once it was found registered", got["sshKeys"])
	}
}

// A token that cannot create keys is the recommended setup, not a
// failure. Say so, drop the key, carry on.
func TestInitFlow_Keys_ForbiddenUploadDegrades(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"cc:dd"}
	s.decisions[promptUploadKey] = true
	s.decisions[promptUploadKeyName] = "old-desktop"
	reg := &fakeRegistry{createErr: fmt.Errorf("registering: %w: 403", provider.ErrForbidden)}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("cc:dd", "old", sshkeys.Disk)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v, want the run to continue\n%s", err, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "cloud.digitalocean.com") {
		t.Errorf("output does not say where to register the key by hand:\n%s", out)
	}
	if got := f.values(t, f.basePath); got["sshKeys"] != nil {
		t.Errorf("sshKeys = %v, want the key left out after a refused upload", got["sshKeys"])
	}
}

// A token that cannot read keys drops the whole tier rather than failing.
func TestInitFlow_Keys_ForbiddenListDropsToTier1(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"aa:bb"}
	reg := &fakeRegistry{listErr: fmt.Errorf("listing: %w: 403", provider.ErrForbidden)}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v, want a fall back to local keys\n%s", err, f.out)
	}
	if s.keyOffer.AccountKnown {
		t.Error("AccountKnown = true after a forbidden list")
	}
	if got := f.values(t, f.basePath); !reflect.DeepEqual(got["sshKeys"], []string{"aa:bb"}) {
		t.Errorf("sshKeys = %v, want the local key still usable", got["sshKeys"])
	}
}

// Editing an existing base.pkl offers its keys back, still selected.
func TestInitFlow_Keys_ExistingConfigValuesAreOffered(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys {\n  \"ff:ee\"\n}\n")

	s := newScript()
	s.answers = map[string]any{"template": "python"}
	s.decisions = map[string]any{
		promptPersonalAction: personalChange,
		promptSavePreset:     false,
	}

	if err := f.runWithKeys(t, s, nil, nil); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	var found bool
	for _, c := range s.keyOffer.Choices {
		if c.Fingerprint == "ff:ee" && c.Selected {
			found = true
		}
	}
	if !found {
		t.Errorf("the existing fingerprint was not offered selected: %+v", s.keyOffer.Choices)
	}
	if got := f.values(t, f.basePath); !reflect.DeepEqual(got["sshKeys"], []string{"ff:ee"}) {
		t.Errorf("sshKeys = %v, want the existing key preserved", got["sshKeys"])
	}
}

// Reusing the existing personal settings must not re-ask for keys.
func TestInitFlow_Keys_NotAskedWhenPersonalSettingsAreReused(t *testing.T) {
	f := newInitFixture(t)
	f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys {\n  \"ff:ee\"\n}\n")

	s := newScript()
	s.answers = map[string]any{"template": "python"}
	s.decisions = map[string]any{
		promptPersonalAction: personalUse,
		promptSavePreset:     false,
	}
	if err := f.runWithKeys(t, s, nil, nil); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if s.keysAsked {
		t.Error("the SSH key question was asked despite reusing the existing settings")
	}
}

// The empty answer is the expensive-and-silent failure, so it gets said
// out loud.
func TestInitFlow_Keys_WarnsWhenNoneAreChosen(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent)}, nil); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "no SSH keys") || !strings.Contains(out, "log in") {
		t.Errorf("output does not warn that the instance will be unreachable:\n%s", out)
	}
}

// The default sources must not reach for a token that is not in the
// environment: resolveToken's fallback can block on a hardware key.
func TestDefaultKeySources_NoTokenMeansNoRegistry(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	reg, err := defaultKeySources().registry(context.Background())
	if err != nil {
		t.Fatalf("registry() error = %v", err)
	}
	if reg != nil {
		t.Error("a registry was built with no token in the environment")
	}
}

func TestDefaultKeySources_TokenInEnvBuildsARegistry(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "test-token")
	reg, err := defaultKeySources().registry(context.Background())
	if err != nil {
		t.Fatalf("registry() error = %v", err)
	}
	if reg == nil {
		t.Error("no registry built despite a token in the environment")
	}
}

var _ = config.Values{}

// A fingerprint typed by hand bypasses the choice list, so the upload
// check never sees it. When the account is known, it must still be
// checked -- otherwise the free-text route is the one way to write a key
// that cannot work and hear nothing about it.
func TestInitFlow_Keys_WarnsAboutATypedKeyTheAccountLacks(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"aa:bb", "typed:by:hand"}
	reg := &fakeRegistry{keys: []provider.SSHKey{{Fingerprint: "aa:bb", Name: "laptop"}}}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("aa:bb", "laptop", sshkeys.Agent)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "typed:by:hand") {
		t.Errorf("output does not name the unregistered key:\n%s", out)
	}
	if strings.Contains(out, "aa:bb is not") {
		t.Errorf("output complains about the registered key:\n%s", out)
	}
	// Warned about, not silently dropped: the user asked for it, and it
	// may be a key they are about to register by hand.
	if got := f.values(t, f.basePath); !reflect.DeepEqual(got["sshKeys"], []string{"aa:bb", "typed:by:hand"}) {
		t.Errorf("sshKeys = %v, want both kept", got["sshKeys"])
	}
}

// A key uploaded during this run is registered by the time the check
// runs, so it must not be reported as missing.
func TestInitFlow_Keys_NoWarningForAKeyJustUploaded(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"cc:dd"}
	s.decisions[promptUploadKey] = true
	s.decisions[promptUploadKeyName] = "old-desktop"
	reg := &fakeRegistry{}

	if err := f.runWithKeys(t, s, []sshkeys.Key{localKey("cc:dd", "old", sshkeys.Disk)}, reg); err != nil {
		t.Fatalf("runInitFlow() error = %v\n%s", err, f.out)
	}
	if out := f.out.String(); strings.Contains(out, "not on your provider account") && strings.Contains(out, "up` will") {
		t.Errorf("warned about a key it had just registered:\n%s", out)
	}
}

// Without an account lookup nothing may be claimed about registration.
func TestInitFlow_Keys_NoAccountMeansNoUnregisteredWarning(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{"whatever"}

	if err := f.runWithKeys(t, s, nil, nil); err != nil {
		t.Fatalf("runInitFlow() error = %v", err)
	}
	if out := f.out.String(); strings.Contains(out, "not on your provider account") {
		t.Errorf("claimed a key is unregistered without having looked:\n%s", out)
	}
}
