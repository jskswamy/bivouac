package sshidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jskswamy/bivouac/internal/xdg"
	"golang.org/x/crypto/ssh/knownhosts"
)

const includeBegin = "# BEGIN bivouac managed SSH include\n"
const includeEnd = "# END bivouac managed SSH include\n"

type ProfileStore struct{ ConfigDir, SSHDir string }

func DefaultProfileStore() (ProfileStore, error) {
	dir, err := xdg.Path(xdg.Config)
	if err != nil {
		return ProfileStore{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ProfileStore{}, err
	}
	return ProfileStore{ConfigDir: dir, SSHDir: filepath.Join(home, ".ssh")}, nil
}
func (s ProfileStore) profileDir() string  { return filepath.Join(s.ConfigDir, "ssh") }
func (s ProfileStore) userPath() string    { return filepath.Join(s.SSHDir, "config") }
func (s ProfileStore) managedPath() string { return filepath.Join(s.profileDir(), "config") }

func readRegular(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("SSH config %s is not a regular file", path)
	}
	// #nosec G304 -- the user/managed SSH configuration path is intentionally read.
	raw, err := os.ReadFile(path)
	return raw, info.Mode().Perm(), err
}

// userConfig is the user's SSH config read through any symlink. Writes go to
// target, never to the link: replacing a link would detach the file from
// whatever (Home Manager, a dotfiles repo) manages it.
type userConfig struct {
	raw    []byte
	mode   os.FileMode
	target string
	// included is true when the config already pulls in the managed file by
	// a plain Include line outside the marked block, e.g. declared in Nix.
	included bool
}

func (s ProfileStore) readUser() (userConfig, error) {
	target := s.userPath()
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return userConfig{target: target, mode: 0o600}, nil
	} else if err != nil {
		return userConfig{}, err
	}
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		return userConfig{}, err
	}
	raw, mode, err := readRegular(target)
	if err != nil {
		return userConfig{}, err
	}
	managed, err := filepath.Abs(s.managedPath())
	if err != nil {
		return userConfig{}, err
	}
	user, included := stripPlainInclude(raw, managed)
	return userConfig{raw: user, mode: mode, target: target, included: included}, nil
}

// stripPlainInclude drops the managed file from Include lines, which the
// profile being checked already supplies inline; leaving it in would count
// its IdentityFile twice. Include takes several paths (Home Manager joins
// programs.ssh.includes into one line), so only the managed token goes and
// the others stay.
func stripPlainInclude(raw []byte, managed string) ([]byte, bool) {
	quoted, _ := quoteSSH(managed)
	var kept []string
	found := false
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Include") {
			kept = append(kept, line)
			continue
		}
		var rest []string
		for _, f := range fields[1:] {
			if f == managed || f == quoted {
				found = true
				continue
			}
			rest = append(rest, f)
		}
		if len(rest) > 0 {
			kept = append(kept, "Include "+strings.Join(rest, " ")+"\n")
		}
	}
	return []byte(strings.Join(kept, "")), found
}

// needsWrite reports whether bivouac must edit the file, and fails with the
// line to add when it must but cannot.
func (s ProfileStore) needsWrite(u userConfig) (bool, error) {
	if u.included {
		return false, nil
	}
	if u.mode&0o200 != 0 {
		return true, nil
	}
	managed, err := filepath.Abs(s.managedPath())
	if err != nil {
		return false, err
	}
	quoted, err := quoteSSH(managed)
	if err != nil {
		return false, err
	}
	return false, fmt.Errorf("SSH config %s is read-only; for bivouac to work, add this line to it:\n\n  Include %s", u.target, quoted)
}

func stripInclude(raw []byte) ([]byte, error) {
	begin := bytes.Index(raw, []byte(includeBegin))
	if begin < 0 {
		if bytes.Contains(raw, []byte(includeEnd)) {
			return nil, fmt.Errorf("malformed bivouac SSH include block; repair the marked block manually")
		}
		return raw, nil
	}
	end := bytes.Index(raw[begin:], []byte(includeEnd))
	if end < 0 || bytes.Count(raw, []byte(includeBegin)) != 1 || bytes.Count(raw, []byte(includeEnd)) != 1 {
		return nil, fmt.Errorf("malformed bivouac SSH include block; repair the marked block manually")
	}
	end += begin + len(includeEnd)
	return append(append([]byte{}, raw[:begin]...), raw[end:]...), nil
}

// Check uses actual OpenSSH evaluation, not a parser that could overlook
// additive user IdentityFile directives or Host/Match scope.
func (s ProfileStore) Check(ctx context.Context, c Connection) error {
	u, err := s.readUser()
	if err != nil {
		return err
	}
	if _, err := s.needsWrite(u); err != nil {
		return err
	}
	user, err := stripInclude(u.raw)
	if err != nil {
		return err
	}
	return s.checkUser(ctx, c, user)
}
func (s ProfileStore) checkUser(ctx context.Context, c Connection, user []byte) error {
	profile, err := renderProfile(c)
	if err != nil {
		return err
	}
	paths, err := identityPaths(c)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "bivouac-ssh-check-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	userPath := filepath.Join(dir, "user-config")
	if err := os.WriteFile(userPath, user, 0o600); err != nil {
		return err
	}
	quoted, err := quoteSSH(userPath)
	if err != nil {
		return err
	}
	text := profile + "Host *\nInclude " + quoted + "\nHost *\n"
	// -F suppresses system configuration; explicitly include it to check the
	// same additive settings the ordinary ssh process will see.
	if _, err := os.Stat("/etc/ssh/ssh_config"); err == nil {
		text += "Include /etc/ssh/ssh_config\n"
	}
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	// #nosec G204 -- fixed ssh executable/options, own temporary config path,
	// and a machine alias already validated by renderProfile; no shell.
	output, err := exec.CommandContext(ctx, "ssh", "-G", "-F", path, c.Alias).CombinedOutput()
	if err != nil {
		return fmt.Errorf("checking effective OpenSSH settings: %w: %s", err, output)
	}
	settings := map[string][]string{}
	for _, line := range strings.Split(string(output), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if ok {
			settings[name] = append(settings[name], strings.TrimSpace(value))
		}
	}
	host, port, err := endpoint(c.PreferredHost)
	if err != nil {
		return err
	}
	for name, want := range map[string]string{"hostname": host, "port": port, "user": c.User, "hostkeyalias": knownhosts.Normalize(net.JoinHostPort(host, port)), "identitiesonly": "yes"} {
		if got := settings[name]; len(got) != 1 || got[0] != want {
			return fmt.Errorf("effective SSH %s conflicts with selected connection (want %q, got %v); scope your user/system SSH settings away from %s", name, want, got, c.Alias)
		}
	}
	gotPaths := settings["identityfile"]
	if len(gotPaths) != len(paths) {
		return fmt.Errorf("effective IdentityFile directives conflict with selected identities (%v); scope unrelated user/system IdentityFile settings away from %s", gotPaths, c.Alias)
	}
	for i, path := range paths {
		// ssh -G retains config quoting and percent escapes until expansion at
		// connection time; compare the safe rendered token as well as raw paths.
		quoted, _ := quoteSSH(path)
		if gotPaths[i] != path && gotPaths[i] != quoted && gotPaths[i] != strings.Trim(quoted, "\"") {
			return fmt.Errorf("effective IdentityFile %q conflicts with selected %q for %s", gotPaths[i], path, c.Alias)
		}
	}
	for _, value := range settings["stricthostkeychecking"] {
		if value == "false" || value == "no" || value == "off" {
			return fmt.Errorf("StrictHostKeyChecking disables host-key checks for %s; repair conflicting SSH settings", c.Alias)
		}
	}
	return nil
}

func (s ProfileStore) lock(ctx context.Context) (func(), error) {
	dir := s.profileDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "lock")
	// #nosec G304 -- lock is a fixed filename in the owned profile directory.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func atomicFile(path string, raw []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bivouac-ssh-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func (s ProfileStore) profiles() (map[string]Connection, error) {
	raw, _, err := readRegular(filepath.Join(s.profileDir(), "profiles.json"))
	if err != nil {
		return nil, err
	}
	profiles := map[string]Connection{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &profiles); err != nil {
			return nil, fmt.Errorf("reading owned SSH profile index: %w", err)
		}
	}
	return profiles, nil
}
func (s ProfileStore) publish(profiles map[string]Connection) error {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	var text strings.Builder
	for _, name := range names {
		entry, err := renderProfile(profiles[name])
		if err != nil {
			return err
		}
		text.WriteString(entry)
	}
	raw, err := json.Marshal(profiles)
	if err != nil {
		return err
	}
	// The index is authoritative: if publication is interrupted, retry renders
	// every machine again without losing another process's successful update.
	if err := atomicFile(filepath.Join(s.profileDir(), "profiles.json"), raw, 0o600); err != nil {
		return err
	}
	return atomicFile(s.managedPath(), []byte(text.String()), 0o600)
}
func (s ProfileStore) Upsert(ctx context.Context, c Connection) error {
	// Fail read-only before creating even the lock directory on conflicts.
	if err := s.Check(ctx, c); err != nil {
		return err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	u, err := s.readUser()
	if err != nil {
		return err
	}
	write, err := s.needsWrite(u)
	if err != nil {
		return err
	}
	user, err := stripInclude(u.raw)
	if err != nil {
		return err
	}
	if err := s.checkUser(ctx, c, user); err != nil {
		return err
	}
	profiles, err := s.profiles()
	if err != nil {
		return err
	}
	profiles[c.Name] = c
	if err := s.publish(profiles); err != nil {
		return err
	}
	if !write {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(u.target), 0o700); err != nil {
		return err
	}
	managed, err := filepath.Abs(s.managedPath())
	if err != nil {
		return err
	}
	quoted, err := quoteSSH(managed)
	if err != nil {
		return err
	}
	block := includeBegin + "Host *\nInclude " + quoted + "\nHost *\n" + includeEnd
	return atomicFile(u.target, append([]byte(block), user...), u.mode)
}
func (s ProfileStore) Remove(ctx context.Context, name string) error {
	if _, err := Alias(name); err != nil {
		return err
	}
	if _, err := os.Stat(s.profileDir()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	profiles, err := s.profiles()
	if err != nil {
		return err
	}
	delete(profiles, name)
	return s.publish(profiles)
}
