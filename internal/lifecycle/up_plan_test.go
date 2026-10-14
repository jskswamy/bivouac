package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/keyplan"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
	"golang.org/x/crypto/ssh"
)

func (f *fakeProvider) ListKeys(context.Context) ([]provider.SSHKey, error) {
	return f.accountKeys, f.keyListErr
}
func (f *fakeProvider) CreateKey(_ context.Context, name, pub string) (provider.SSHKey, error) {
	f.keyWrites++
	if f.keyCreateErr != nil {
		return provider.SSHKey{}, f.keyCreateErr
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return provider.SSHKey{}, err
	}
	key := provider.SSHKey{ID: "42", Name: name, Fingerprint: ssh.FingerprintLegacyMD5(public), PublicKey: pub}
	f.accountKeys = append(f.accountKeys, key)
	return key, nil
}

func upFixture(t *testing.T, ctx context.Context, p *fakeProvider, steps Steps, name, path, root string) error {
	t.Helper()
	cfg, err := config.Resolve(ctx, path)
	if err != nil {
		return err
	}
	keys, err := keyplan.Build(ctx, p, []sshkeys.Key{testenv.SSHKey(t)})
	if err != nil {
		return err
	}
	plan, err := PrepareUp(ctx, name, path, root, cfg, keys)
	if err != nil {
		return err
	}
	return Up(ctx, p, steps, plan)
}

func planFixture(t *testing.T, p *fakeProvider) (UpPlan, sshkeys.Key) {
	t.Helper()
	key := testenv.SSHKey(t)
	root := t.TempDir()
	path := minimalBivouacPkl(t, root)
	cfg, err := config.Resolve(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := keyplan.Build(context.Background(), p, []sshkeys.Key{key})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareUp(context.Background(), "machine", path, root, cfg, keys)
	if err != nil {
		t.Fatal(err)
	}
	return plan, key
}

func TestApprovedUpPersistsBeforeReadiness(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	p := &fakeProvider{vm: provider.VM{ID: "vm", IP: "192.0.2.1"}}
	plan, _ := planFixture(t, p)
	// Raw config changes after approval must not change the creation spec.
	writeFixture(t, plan.ConfigPath, "region = \"sfo3\"\nsize = \"changed\"\ntemplate = \"changed\"\n")
	steps := Steps{WaitReady: func(ctx context.Context, ip, user string, _ time.Duration) error {
		store, err := state.Open()
		if err != nil {
			return err
		}
		record, ok, err := store.Get("machine")
		if err != nil {
			return err
		}
		if !ok || record.SSH == nil || record.SSH.Alias != "bivouac-machine" || p.keyWrites != 1 {
			t.Fatal("metadata or registration not recorded before readiness")
		}
		if _, ok := sshidentity.FromContext(ctx); !ok {
			t.Fatal("readiness not scoped")
		}
		return nil
	}, Reconcile: func(context.Context, string, string) error { return nil }}
	if err := Up(context.Background(), p, steps, plan); err != nil {
		t.Fatal(err)
	}
	if p.gotSpec.Region != "nyc3" || len(p.gotSpec.SSHKeys) != 1 || p.gotSpec.SSHKeys[0] != "42" {
		t.Fatalf("spec=%+v", p.gotSpec)
	}
}

func TestApprovedUpRevalidatesBeforeAnyWrite(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	p := &fakeProvider{vm: provider.VM{ID: "vm", IP: "192.0.2.1"}}
	plan, key := planFixture(t, p)
	if err := os.WriteFile(key.Path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Up(context.Background(), p, Steps{}, plan); err == nil || p.created || p.keyWrites != 0 {
		t.Fatal("mutated plan made writes")
	}
}

func TestApprovedUpRegistrationAndProfileFailure(t *testing.T) {
	for _, profileFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(profileFailure), func(t *testing.T) {
			testenv.Isolate(t)
			t.Setenv("SSH_AUTH_SOCK", "")
			p := &fakeProvider{vm: provider.VM{ID: "vm", IP: "192.0.2.1"}}
			plan, _ := planFixture(t, p)
			if profileFailure {
				p.onCreate = func() {
					home, err := os.UserHomeDir()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
						t.Fatal(err)
					}
					writeFixture(t, filepath.Join(home, ".ssh", "config"), "Host *\n IdentityFile /unrelated\n")
				}
			} else {
				p.keyCreateErr = errors.New("registration refused")
			}
			if err := Up(context.Background(), p, Steps{}, plan); err == nil {
				t.Fatal("failure reported as success")
			}
			if !profileFailure && p.created {
				t.Fatal("droplet created after registration failure")
			}
			if profileFailure {
				store, err := state.Open()
				if err != nil {
					t.Fatal(err)
				}
				record, ok, err := store.Get("machine")
				if err != nil || !ok || record.SSH == nil || !p.created {
					t.Fatal("profile failure left VM untracked")
				}
			}
		})
	}
}
func TestApprovedUpStateFailureRollsBackVM(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	p := &fakeProvider{vm: provider.VM{ID: "vm", IP: "192.0.2.1"}}
	plan, _ := planFixture(t, p)
	p.onCreate = func() {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, ".local", "state")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(dir, "bivouac"), "blocked state directory")
	}
	if err := Up(context.Background(), p, Steps{}, plan); err == nil || !p.destroyed || p.destroyedID != "vm" {
		t.Fatalf("rollback=%v destroyed=%v", err, p.destroyed)
	}
}
