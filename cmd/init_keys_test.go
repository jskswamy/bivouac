package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

type fakeRegistry struct {
	keys               []provider.SSHKey
	listErr, createErr error
	createdButListed   *provider.SSHKey
	created            []provider.SSHKey
}

func (f *fakeRegistry) ListKeys(context.Context) ([]provider.SSHKey, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := append([]provider.SSHKey{}, f.keys...)
	if f.createdButListed != nil {
		out = append(out, *f.createdButListed)
	}
	return out, nil
}
func (f *fakeRegistry) CreateKey(_ context.Context, name, publicKey string) (provider.SSHKey, error) {
	if f.createErr != nil {
		return provider.SSHKey{}, f.createErr
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return provider.SSHKey{}, err
	}
	k := provider.SSHKey{ID: "new", Name: name, PublicKey: publicKey, Fingerprint: ssh.FingerprintLegacyMD5(pub)}
	f.created = append(f.created, k)
	f.keys = append(f.keys, k)
	return k, nil
}

func setupKey(t *testing.T) sshkeys.Key {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "custom key.pub")
	if err := os.WriteFile(path, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".pub"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := sshkeys.ReadPublicFile(sshkeys.FileRef{Entry: path, Path: path, ConfigPath: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func setupSources(key sshkeys.Key, reg provider.KeyRegistry) keySources {
	return keySources{local: func() ([]sshkeys.Key, error) { return []sshkeys.Key{key}, nil }, registry: func(context.Context) (provider.KeyRegistry, error) { return reg, nil }}
}
func baseScript() *scriptedPrompter {
	s := newScript()
	s.answers = map[string]any{"region": "nyc3", "size": "s-1vcpu-1gb", "template": "python"}
	s.decisions = map[string]any{promptSavePreset: false}
	return s
}

func TestAskSSHKeys_StoresSelectedPath(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	f := newInitFixture(t)
	key := setupKey(t)
	s := baseScript()
	s.keyChoice = []string{key.Path}
	if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, nil), keySetupStandalone); err != nil {
		t.Fatal(err)
	}
	got := f.values(t, f.basePath)
	if !reflect.DeepEqual(got[config.FieldSSHKeys], []string{key.Path}) {
		t.Fatalf("keys=%v", got[config.FieldSSHKeys])
	}
	if strings.Contains(f.text(t, f.basePath), key.Fingerprint) || strings.Contains(f.text(t, f.project), key.Path) {
		t.Fatal("wrong storage location/reference")
	}
	if !strings.Contains(f.out.String(), "deferred") {
		t.Fatalf("missing deferred verification: %s", f.out)
	}
}

func TestAskSSHKeys_ExplicitUploadPolicy(t *testing.T) {
	for _, policy := range []keySetupPolicy{keySetupStandalone, keySetupForUp} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", "")
			f := newInitFixture(t)
			key := setupKey(t)
			s := baseScript()
			s.keyChoice = []string{key.Path}
			s.decisions[promptUploadKey] = true
			reg := &fakeRegistry{}
			if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, reg), policy); err != nil {
				t.Fatal(err)
			}
			want := 0
			if policy == keySetupStandalone {
				want = 1
			}
			if len(reg.created) != want {
				t.Fatalf("writes=%d want=%d", len(reg.created), want)
			}
			if want == 1 && reg.created[0].Name != "bivouac-custom key" {
				t.Fatalf("name=%q", reg.created[0].Name)
			}
		})
	}
}

func TestAskSSHKeys_DeclinedOrFailedUploadKeepsPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		confirm bool
		err     error
	}{{"decline", false, nil}, {"forbidden", true, provider.ErrForbidden}, {"failed", true, errors.New("provider failed")}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", "")
			f := newInitFixture(t)
			key := setupKey(t)
			s := baseScript()
			s.keyChoice = []string{key.Path}
			s.decisions[promptUploadKey] = tc.confirm
			reg := &fakeRegistry{createErr: tc.err}
			if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, reg), keySetupStandalone); err != nil {
				t.Fatal(err)
			}
			if len(reg.created) != 0 {
				t.Fatal("unexpected upload")
			}
			if !reflect.DeepEqual(f.values(t, f.basePath)[config.FieldSSHKeys], []string{key.Path}) {
				t.Fatal("selection silently dropped")
			}
		})
	}
}

func TestAskSSHKeys_RegistrationRaceAndReadFailure(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprint(race), func(t *testing.T) {
			f := newInitFixture(t)
			key := setupKey(t)
			s := baseScript()
			s.keyChoice = []string{key.Path}
			s.decisions[promptUploadKey] = true
			reg := &fakeRegistry{listErr: provider.ErrForbidden}
			if race {
				reg = &fakeRegistry{keys: []provider.SSHKey{{Fingerprint: key.Fingerprint}}, createErr: errors.New("race")}
			}
			if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, reg), keySetupStandalone); err != nil {
				t.Fatal(err)
			}
			if len(reg.created) != 0 {
				t.Fatal("unexpected create")
			}
			if !reflect.DeepEqual(f.values(t, f.basePath)[config.FieldSSHKeys], []string{key.Path}) {
				t.Fatal("key lost")
			}
		})
	}
}

func TestInit_ReplacesLegacyKeysInOriginalFiles(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(fmt.Sprint(move), func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", "")
			f := newInitFixture(t)
			key := setupKey(t)
			f.write(t, f.basePath, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys { \"12345\" }\n")
			f.write(t, f.project, "template = \"python\"\nsshKeys { \"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\" }\n")
			s := baseScript()
			s.keyChoice = []string{key.Path}
			s.decisions[promptMovePersonal] = move
			s.decisions[promptPersonalAction] = personalUse
			if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, nil), keySetupForUp); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{f.basePath, f.project} {
				raw := f.text(t, path)
				if strings.Contains(raw, "12345") || strings.Contains(raw, "SHA256:") {
					t.Fatalf("legacy reference survived in %s: %s", path, raw)
				}
			}
			if !move && !reflect.DeepEqual(f.values(t, f.project)[config.FieldSSHKeys], []string{key.Path}) {
				t.Fatal("declined move relocated project keys")
			}
		})
	}
}

func TestInit_RepairsOverriddenBase(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	f := newInitFixture(t)
	key := setupKey(t)
	custom := filepath.Join(f.root, "personal", "custom.pkl")
	f.write(t, custom, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys { \"12345\" }\n")
	f.write(t, f.project, "basePath = \"./personal/custom.pkl\"\ntemplate = \"python\"\n")
	s := baseScript()
	s.keyChoice = []string{key.Path}
	s.decisions[promptPersonalAction] = personalUse
	if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, nil), keySetupForUp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.values(t, custom)[config.FieldSSHKeys], []string{key.Path}) {
		t.Fatal("did not repair original overridden file")
	}
	if _, err := os.Stat(f.basePath); !os.IsNotExist(err) {
		t.Fatalf("unexpected default base file: %v", err)
	}
}

func TestAskSSHKeys_AgentOnlyExplainsExport(t *testing.T) {
	f := newInitFixture(t)
	s := baseScript()
	s.keyChoice = []string{}
	key := sshkeys.Key{Source: sshkeys.Agent, Comment: "hardware"}
	if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, nil), keySetupStandalone); err != nil {
		t.Fatal(err)
	}
	if len(s.keyOffer.Choices) != 0 || !strings.Contains(f.out.String(), "ssh-add -L") {
		t.Fatalf("agent-only key offered without file: %s", f.out)
	}
}

func TestAskSSHKeys_InvalidManualPathAndEmptyChoice(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for _, entry := range []string{"12345", "/nonexistent/key.pub"} {
		f := newInitFixture(t)
		key := setupKey(t)
		s := baseScript()
		s.keyChoice = []string{entry}
		if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, nil), keySetupStandalone); err == nil {
			t.Fatalf("invalid input %q accepted", entry)
		}
	}
	f := newInitFixture(t)
	key := setupKey(t)
	s := baseScript()
	s.keyChoice = []string{}
	if err := runInitFlowWith(context.Background(), f.out, s, f.root, setupSources(key, nil), keySetupStandalone); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "no SSH keys") {
		t.Fatal("missing empty-selection warning")
	}
}

func TestDefaultKeySources_NoTokenMeansNoRegistry(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	reg, err := defaultKeySources().registry(context.Background())
	if err != nil || reg != nil {
		t.Fatalf("registry=%v err=%v", reg, err)
	}
}
func TestDefaultKeySources_TokenInEnvBuildsARegistry(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "test-token")
	reg, err := defaultKeySources().registry(context.Background())
	if err != nil || reg == nil {
		t.Fatalf("registry=%v err=%v", reg, err)
	}
}
