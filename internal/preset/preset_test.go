package preset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func tempXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestDir_SitsBesideTheBaseConfig(t *testing.T) {
	xdg := tempXDG(t)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	want := filepath.Join(xdg, "cloudlab", "presets")
	if got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestSaveLoad_RoundTrips(t *testing.T) {
	tempXDG(t)
	ctx := context.Background()

	values := config.Values{
		"template": "python",
		"agents":   []string{"claude"},
		"packages": []string{"ripgrep", "jq"},
		"beads":    "session",
		"size":     "s-4vcpu-8gb",
	}
	if err := Save("go-api", values); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(ctx, "go-api")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, values) {
		t.Errorf("Load() = %#v, want %#v", got, values)
	}
}

func TestSave_CreatesTheDirectoryOnFirstUse(t *testing.T) {
	tempXDG(t)
	if err := Save("first", config.Values{"template": "docker"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	dir, _ := Dir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("preset dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("preset dir is not a directory")
	}
}

func TestSave_OverwritesAnExistingPreset(t *testing.T) {
	tempXDG(t)
	ctx := context.Background()

	if err := Save("shape", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := Save("shape", config.Values{"template": "docker"}); err != nil {
		t.Fatalf("Save() second error = %v", err)
	}

	got, err := Load(ctx, "shape")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, config.Values{"template": "docker"}) {
		t.Errorf("Load() = %#v, want the second save's values", got)
	}
}

func TestList_SortedWithWhatEachSets(t *testing.T) {
	tempXDG(t)
	ctx := context.Background()

	if err := Save("zeta", config.Values{"template": "docker"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := Save("alpha", config.Values{"template": "python", "agents": []string{"claude"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List() returned %d presets, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("List() names = %q, %q; want alpha, zeta in order", got[0].Name, got[1].Name)
	}
	if !reflect.DeepEqual(got[0].Values, config.Values{"template": "python", "agents": []string{"claude"}}) {
		t.Errorf("alpha values = %#v", got[0].Values)
	}
	if got[0].Modified.IsZero() {
		t.Error("alpha has no modification time; list has nothing to show in its last column")
	}
}

// An empty store is the first-run case, not a failure.
func TestList_NoPresetsIsNotAnError(t *testing.T) {
	tempXDG(t)
	got, err := List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() = %v, want empty", got)
	}
}

// Only .pkl files are presets; anything else the directory collects is
// not one, and reporting it as a broken preset would be noise.
func TestList_IgnoresNonPklEntries(t *testing.T) {
	tempXDG(t)
	if err := Save("real", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	dir, _ := Dir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing decoy: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir.pkl"), 0o700); err != nil {
		t.Fatalf("making decoy dir: %v", err)
	}

	got, err := List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Errorf("List() = %+v, want just real", got)
	}
}

func TestLoad_MissingPresetIsNotFound(t *testing.T) {
	tempXDG(t)
	_, err := Load(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want ErrNotFound", err)
	}
}

func TestDelete_RemovesTheFile(t *testing.T) {
	tempXDG(t)
	if err := Save("gone", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := Delete("gone"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := Load(context.Background(), "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Load() error = %v, want ErrNotFound", err)
	}
}

func TestDelete_MissingPresetIsNotFound(t *testing.T) {
	tempXDG(t)
	if err := Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete() error = %v, want ErrNotFound", err)
	}
}

// A preset name becomes a filename. Anything that could climb out of
// the preset directory, or name something other than a plain file in
// it, is refused before it reaches the filesystem.
func TestNames_RejectedBeforeTouchingTheFilesystem(t *testing.T) {
	tempXDG(t)
	for _, name := range []string{
		"",
		" ",
		".",
		"..",
		"../escape",
		"sub/dir",
		"back\\slash",
		".hidden",
		"with space",
		"trailing.",
		"has.pkl.pkl",
	} {
		t.Run(name, func(t *testing.T) {
			if err := Save(name, config.Values{"template": "python"}); err == nil {
				t.Errorf("Save(%q) error = nil, want a rejection", name)
			}
			if _, err := Load(context.Background(), name); err == nil {
				t.Errorf("Load(%q) error = nil, want a rejection", name)
			}
			if err := Delete(name); err == nil {
				t.Errorf("Delete(%q) error = nil, want a rejection", name)
			}
		})
	}
}

func TestNames_Accepted(t *testing.T) {
	tempXDG(t)
	for _, name := range []string{"go-api", "k8s_dev", "Rust2", "a"} {
		t.Run(name, func(t *testing.T) {
			if err := Save(name, config.Values{"template": "python"}); err != nil {
				t.Errorf("Save(%q) error = %v, want it accepted", name, err)
			}
		})
	}
}

// The preset store holds personal choices; it should not be created
// world-readable.
func TestSave_DirectoryAndFileArePrivate(t *testing.T) {
	tempXDG(t)
	if err := Save("private", config.Values{"template": "python"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	dir, _ := Dir()
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("preset dir mode = %o, want no group/other bits", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "private.pkl"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("preset file mode = %o, want no group/other bits", perm)
	}
}

// Save must produce a file the config loader accepts: a preset that
// cannot be read back is worse than no preset.
func TestSave_ProducesAReadableFile(t *testing.T) {
	tempXDG(t)
	values := config.Values{
		"template":  "python",
		"tailscale": true,
		"packages":  []string{"jq"},
		"flakes":    []config.Flake{{Url: "github:foo/bar", Packages: []string{"default"}, Modules: true}},
	}
	if err := Save("full", values); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(context.Background(), "full")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, values) {
		t.Errorf("Load() = %#v, want %#v", got, values)
	}
}
