package keyplan

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

type registry struct {
	keys, created      []provider.SSHKey
	listErr, createErr error
	race               bool
	failAt             int
}

func (r *registry) ListKeys(context.Context) ([]provider.SSHKey, error) {
	return append([]provider.SSHKey(nil), r.keys...), r.listErr
}
func (r *registry) CreateKey(_ context.Context, name, pub string) (provider.SSHKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return provider.SSHKey{}, err
	}
	k := provider.SSHKey{ID: "123", Name: name, PublicKey: pub, Fingerprint: ssh.FingerprintLegacyMD5(key)}
	if r.race {
		r.keys = append(r.keys, k)
		return provider.SSHKey{}, errors.New("create race")
	}
	if r.createErr != nil {
		return provider.SSHKey{}, r.createErr
	}
	if r.failAt > 0 && len(r.created)+1 == r.failAt {
		return provider.SSHKey{}, errors.New("partial failure")
	}
	r.created = append(r.created, k)
	r.keys = append(r.keys, k)
	return k, nil
}
func keyFixture(t *testing.T) sshkeys.Key {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom key.pub")
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
func TestBuildReadOnlyAndRegistration(t *testing.T) {
	key := keyFixture(t)
	reg := &registry{}
	plan, err := Build(context.Background(), reg, []sshkeys.Key{key, key})
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.created) != 0 || len(plan.Entries) != 1 || plan.Entries[0].Registered || plan.Entries[0].AccountName != "bivouac-custom key" {
		t.Fatalf("plan=%+v writes=%v", plan, reg.created)
	}
	ids, err := Register(context.Background(), reg, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || len(reg.created) != 1 {
		t.Fatalf("ids=%v writes=%v", ids, reg.created)
	}
	if _, err := Register(context.Background(), reg, plan); err != nil || len(reg.created) != 1 {
		t.Fatalf("retry wrote again: %v", err)
	}
}
func TestRegisteredReuseAndReadErrors(t *testing.T) {
	key := keyFixture(t)
	reg := &registry{keys: []provider.SSHKey{{ID: "9", Fingerprint: key.Fingerprint, Name: "user-owned name"}}}
	plan, err := Build(context.Background(), reg, []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Entries[0].Registered || plan.Entries[0].AccountName != "user-owned name" {
		t.Fatal("registered name changed")
	}
	if _, err := Register(context.Background(), reg, plan); err != nil || len(reg.created) != 0 {
		t.Fatalf("registered key modified: %v", err)
	}
	for _, failure := range []error{provider.ErrForbidden, errors.New("network")} {
		reg.listErr = failure
		if _, err := Build(context.Background(), reg, []sshkeys.Key{key}); err == nil {
			t.Fatal("read error treated as empty registry")
		}
	}
}
func TestRegisterMutationRaceAndPermissionErrors(t *testing.T) {
	key := keyFixture(t)
	reg := &registry{}
	plan, err := Build(context.Background(), reg, []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	reg.race = true
	if _, err := Register(context.Background(), reg, plan); err != nil {
		t.Fatal(err)
	}
	reg = &registry{createErr: provider.ErrForbidden}
	if _, err := Register(context.Background(), reg, plan); err == nil || !strings.Contains(err.Error(), "doctl compute ssh-key import") || !strings.Contains(err.Error(), key.Path) {
		t.Fatalf("permission error=%v", err)
	}
	if err := os.WriteFile(key.Path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg = &registry{}
	if _, err := Register(context.Background(), reg, plan); err == nil || len(reg.created) != 0 {
		t.Fatal("mutation wrote remotely")
	}
}
func TestPartialRegistrationIsRetainedAndRetryable(t *testing.T) {
	one := keyFixture(t)
	two := keyFixture(t)
	reg := &registry{failAt: 2}
	plan, err := Build(context.Background(), reg, []sshkeys.Key{one, two})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Register(context.Background(), reg, plan); err == nil || len(reg.created) != 1 {
		t.Fatalf("partial registration=%v writes=%v", err, reg.created)
	}
	reg.failAt = 0
	if _, err := Register(context.Background(), reg, plan); err != nil || len(reg.created) != 2 {
		t.Fatalf("retry=%v writes=%v", err, reg.created)
	}
}
