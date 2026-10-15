package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func (s *Store) LockSession(ctx context.Context, instance, session string) (func() error, error) {
	return acquireFileLock(ctx, s.lockPath("session", instance+"\x00"+session), true)
}

func (s *Store) LockInstance(ctx context.Context, instance string, exclusive bool) (func() error, error) {
	return acquireFileLock(ctx, s.lockPath("instance", instance), exclusive)
}

func (s *Store) lockState(ctx context.Context, exclusive bool) (func() error, error) {
	stateDir := filepath.Dir(s.path)
	// Keep the lock beside the bivouac directory, not inside it. Reads of a
	// missing store must not create that directory: Up relies on a later
	// state-directory failure still being observable after its preflight read.
	path := filepath.Join(filepath.Dir(stateDir), "."+filepath.Base(stateDir)+"-"+filepath.Base(s.path)+".lock")
	return acquireFileLock(ctx, path, exclusive)
}

func (s *Store) lockPath(kind, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(filepath.Dir(s.path), "locks", kind+"-"+hex.EncodeToString(sum[:16])+".lock")
}

func acquireFileLock(ctx context.Context, path string, exclusive bool) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// #nosec G304 -- callers derive path from the Store path and hashed identities.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	for {
		err = unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
		if err == nil {
			var once sync.Once
			var releaseErr error
			return func() error {
				once.Do(func() {
					if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
						releaseErr = err
					}
					if err := file.Close(); releaseErr == nil && err != nil {
						releaseErr = err
					}
				})
				return releaseErr
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
