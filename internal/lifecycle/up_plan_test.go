package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/image"
	"github.com/jskswamy/bivouac/internal/keyplan"
	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provisioning"
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
	plan, err := PrepareUp(ctx, name, path, root, cfg, keys, stockImage(t, cfg))
	if err != nil {
		return err
	}
	return Up(ctx, p, steps, plan)
}

func planFixture(t *testing.T, p *fakeProvider) (UpPlan, sshkeys.Key) {
	t.Helper()
	plan, key, err := prepareFixture(t, p, func(cfg *config.Resolved) image.Resolution { return stockImage(t, *cfg) })
	if err != nil {
		t.Fatal(err)
	}
	return plan, key
}

// prepareFixture runs PrepareUp on the minimal project, letting the
// caller adjust the resolved config and pick the image resolution.
func prepareFixture(t *testing.T, p *fakeProvider, img func(*config.Resolved) image.Resolution) (UpPlan, sshkeys.Key, error) {
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
	res := img(&cfg)
	plan, err := PrepareUp(context.Background(), "machine", path, root, cfg, keys, res)
	return plan, key, err
}

// stockImage resolves cfg's image the way up does for a stock slug: with
// no image manager at all, which a stock slug never consults.
func stockImage(t *testing.T, cfg config.Resolved) image.Resolution {
	t.Helper()
	res, err := image.Resolve(context.Background(), nil, image.Request{Ref: cfg.Image, Arch: cfg.Arch, Region: cfg.Region, Size: cfg.Size, Shell: cfg.Shell})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func bakedImage(cfg *config.Resolved, shell string) image.Resolution {
	cfg.Image = "tag:my-dev"
	return image.Resolution{Requested: cfg.Image, Image: "123", Baked: true, Shell: shell}
}

func TestPrepareUp_StockImageRendersStockBoot(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	plan, _ := planFixture(t, &fakeProvider{})
	if plan.Spec.Image != "ubuntu-24-04-x64" {
		t.Errorf("Spec.Image = %q, want the stock slug", plan.Spec.Image)
	}
	if !strings.Contains(plan.Spec.UserData, "install.determinate.systems") {
		t.Error("stock launch user data does not install Nix")
	}
	if plan.Image.Baked {
		t.Error("stock plan marked baked")
	}
}

func TestPrepareUp_BakedImageRendersBakedBoot(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	plan, _, err := prepareFixture(t, &fakeProvider{}, func(cfg *config.Resolved) image.Resolution {
		cfg.Shell = "fish"
		return bakedImage(cfg, "fish")
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Spec.Image != "123" {
		t.Errorf("Spec.Image = %q, want the resolved ID 123", plan.Spec.Image)
	}
	if strings.Contains(plan.Spec.UserData, "install.determinate.systems") {
		t.Error("baked launch user data reinstalls Nix")
	}
	want, err := provisioning.RenderCloudInit(plan.Connection.User, "fish", provisioning.BootBaked)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Spec.UserData != want {
		t.Error("baked launch user data is not the BootBaked render")
	}
	if plan.Image.Image != "123" || !plan.Image.Baked {
		t.Errorf("plan.Image = %+v, want the baked resolution carried", plan.Image)
	}
}

// The image decided the login shell when it was built, and nothing at
// launch changes it, so a config asking for another one must stop here
// rather than boot an instance with the wrong shell.
func TestPrepareUp_BakedShellMismatchFailsNamingBoth(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	plan, _, err := prepareFixture(t, &fakeProvider{}, func(cfg *config.Resolved) image.Resolution {
		cfg.Shell = "fish"
		return bakedImage(cfg, "zsh")
	})
	if err == nil {
		t.Fatal("PrepareUp() error = nil, want a shell mismatch")
	}
	for _, want := range []string{"zsh", "fish"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if !reflect.DeepEqual(plan, UpPlan{}) {
		t.Errorf("PrepareUp() returned a plan alongside the error: %+v", plan)
	}
}

// A resolution for some other reference than the config's would launch
// an image nobody asked for.
func TestPrepareUp_ResolutionForAnotherRefFails(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	_, _, err := prepareFixture(t, &fakeProvider{}, func(cfg *config.Resolved) image.Resolution {
		return image.Resolution{Requested: "tag:other", Image: "9", Baked: true, Shell: cfg.Shell}
	})
	if err == nil || !strings.Contains(err.Error(), "tag:other") {
		t.Fatalf("PrepareUp() error = %v, want it to name the mismatched reference", err)
	}
}

func TestUp_RecordsRequestedAndResolvedImage(t *testing.T) {
	for _, tc := range []struct {
		name            string
		baked           bool
		wantRef, wantID string
	}{
		{"stock", false, "ubuntu-24-04-x64", ""},
		{"baked", true, "tag:my-dev", "123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.Isolate(t)
			t.Setenv("SSH_AUTH_SOCK", "")
			p := &fakeProvider{vm: provider.VM{ID: "vm", IP: "192.0.2.1"}}
			plan, _, err := prepareFixture(t, p, func(cfg *config.Resolved) image.Resolution {
				if tc.baked {
					return bakedImage(cfg, cfg.Shell)
				}
				return stockImage(t, *cfg)
			})
			if err != nil {
				t.Fatal(err)
			}
			steps := Steps{WaitReady: func(context.Context, string, string, time.Duration) error { return nil }, Reconcile: func(context.Context, string, string) error { return nil }}
			if err := Up(context.Background(), p, steps, plan); err != nil {
				t.Fatal(err)
			}
			store, err := state.Open()
			if err != nil {
				t.Fatal(err)
			}
			record, _, err := store.Get("machine")
			if err != nil {
				t.Fatal(err)
			}
			if record.ImageRef != tc.wantRef || record.ImageID != tc.wantID {
				t.Errorf("record image = %q/%q, want %q/%q", record.ImageRef, record.ImageID, tc.wantRef, tc.wantID)
			}
		})
	}
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
func TestForceDownDoesNotLoadMissingRecordedSigner(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Record{Name: "machine", VMID: "vm", IP: "192.0.2.1", User: "me", SSH: &sshidentity.Connection{Name: "machine", Alias: "bivouac-machine", PublicIP: "192.0.2.1", PreferredHost: "192.0.2.1", User: "me", Keys: []sshidentity.Identity{{PublicPath: "/missing.pub", PrivatePath: "/missing"}}}}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	if err := Down(context.Background(), p, store, record, true); err != nil || !p.destroyed {
		t.Fatalf("force down failed: %v", err)
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
