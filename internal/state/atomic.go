package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

var atomicRename = os.Rename

// WriteFileAtomic publishes a complete file only after its contents reach the
// filesystem. An existing file's permissions are retained.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		if temp != nil {
			if err := temp.Close(); retErr == nil && err != nil {
				retErr = err
			}
		}
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	temp = nil
	if err := atomicRename(tempName, path); err != nil {
		return err
	}
	// #nosec G304 -- dir is the parent of the caller-selected state/journal path.
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		if err := directory.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	if err := directory.Sync(); err != nil {
		return err
	}
	return nil
}
