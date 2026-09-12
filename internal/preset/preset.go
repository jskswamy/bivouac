// Package preset stores reusable sets of wizard answers on disk.
//
// A preset is an ordinary partial pkl file — the same shape as the
// personal base config and as cloudlab.pkl — so there is no new format
// here, only a place to keep them and a name to find them by.
//
// Presets are stamped into a project's cloudlab.pkl when used, never
// referenced from it. Nothing therefore depends on a preset once it has
// been used, which is why Delete needs no dependency check.
package preset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/jskswamy/cloudlab/internal/config"
)

// ErrNotFound reports that no preset of that name is saved.
var ErrNotFound = errors.New("no such preset")

// extension is what marks a file in the preset directory as a preset.
const extension = ".pkl"

// Preset is one saved shape.
type Preset struct {
	// Name is the preset's name, which is its filename without the
	// extension.
	Name string
	// Path is the file it is stored in.
	Path string
	// Modified is the file's modification time — "when it changed" in
	// `preset list`. The file's own mtime rather than recorded
	// metadata: tracking last-used would mean a write on every read.
	Modified time.Time
	// Values are the fields the preset sets.
	Values config.Values
}

// Dir returns the directory presets are stored in, beside the personal
// base config.
func Dir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "presets"), nil
}

// namePattern is what a preset name may be. Deliberately narrower than
// the filesystem allows: the name is a filename, and the cheapest way
// to be sure it can never escape the preset directory or name anything
// unexpected in it is to accept only an unsurprising word. Leading
// dots, separators and traversal are all excluded by construction
// rather than by checking for them one at a time.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Path returns where the preset called name is stored, rejecting any
// name that is not safe to use as a filename.
func Path(name string) (string, error) {
	if !namePattern.MatchString(name) || filepath.Ext(name) != "" {
		return "", fmt.Errorf("%q is not a valid preset name: use letters, digits, dots, dashes and underscores, starting with a letter or digit, and no extension", name)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+extension), nil
}

// Save writes values as the preset called name, replacing any preset
// already saved under that name.
func Save(name string, values config.Values) error {
	path, err := Path(name)
	if err != nil {
		return err
	}
	rendered, err := config.Render(values)
	if err != nil {
		return fmt.Errorf("saving preset %q: %w", name, err)
	}
	// 0o700/0o600: a preset records how someone likes their machines
	// built, which is nobody else's business on a shared host.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("saving preset %q: %w", name, err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("saving preset %q: %w", name, err)
	}
	return nil
}

// Load reads the preset called name.
func Load(ctx context.Context, name string) (config.Values, error) {
	path, err := Path(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return nil, err
	}
	values, err := config.ReadValues(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading preset %q: %w", name, err)
	}
	return values, nil
}

// Delete removes the preset called name.
//
// No dependency check: a preset is stamped into the projects made from
// it rather than linked, so deleting one cannot leave a project unable
// to resolve its config.
func Delete(name string) error {
	path, err := Path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return err
	}
	return nil
}

// List returns every saved preset, by name.
//
// A preset store that does not exist yet is empty, not an error — that
// is simply the state before the first save.
func List(ctx context.Context) ([]Preset, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var presets []Preset
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != extension {
			continue
		}
		name := entry.Name()[:len(entry.Name())-len(extension)]
		if !namePattern.MatchString(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, entry.Name())
		values, err := config.ReadValues(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("reading preset %q: %w", name, err)
		}
		presets = append(presets, Preset{Name: name, Path: path, Modified: info.ModTime(), Values: values})
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].Name < presets[j].Name })
	return presets, nil
}
