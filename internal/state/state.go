package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/xdg"
)

// Record is one instance's state: which provider created it and its VM
// and network details.
type Record struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	Project         string `json:"project,omitempty"`
	VMID            string `json:"vm_id"`
	IP              string `json:"ip"`
	Region          string `json:"region"`
	Size            string `json:"size"`
	Template        string `json:"template"`
	User            string `json:"user"`
	RepoPath        string `json:"repo_path"`
	TailscaleJoined bool   `json:"tailscale_joined"`
	HerdrMachineID  string `json:"herdr_machine_id,omitempty"`
	// SSH is optional for old state. New records bind authentication to the
	// creation-time selection, not later edits to the project configuration.
	SSH *sshidentity.Connection `json:"ssh,omitempty"`
	// Shell is the login shell cloud-init was asked to set at creation.
	// Recorded because nothing applies it afterwards, so a later config
	// edit can be checked against what the instance actually got. Empty
	// means the instance predates the field.
	Shell string `json:"shell,omitempty"`
	// ImageRef is the image reference the config named at creation, and
	// ImageID the image it resolved to when that was a Bivouac-built one.
	// A tag: reference moves as new builds land, so only the ID says what
	// this instance actually booted from. ImageID is empty for a stock
	// slug, which is already concrete; both are empty for instances that
	// predate the fields.
	ImageRef           string              `json:"image_ref,omitempty"`
	ImageID            string              `json:"image_id,omitempty"`
	Sessions           []Session           `json:"sessions"`
	RetirementReceipts []RetirementReceipt `json:"retirement_receipts,omitempty"`
}

// ConnectionContext binds metadata at record ownership boundaries without
// loading signers. Force teardown remains possible if local keys disappear.
func (r Record) ConnectionContext(ctx context.Context) context.Context {
	if r.SSH == nil {
		return sshidentity.WithoutConnection(ctx)
	}
	return sshidentity.WithConnection(ctx, *r.SSH)
}

// Session is one agent session on an instance. Name, LocalRepo and Base
// travel together in one struct rather than as parallel fields: a base
// paired with the wrong session is a silently wrong replay range, which is
// exactly the bug that made merge re-apply a whole rewritten history.
type Session struct {
	Name      string `json:"name"`
	LocalRepo string `json:"local_repo"`
	Base      string `json:"base"`
	// RepoName is the identity the session's remote directory was seeded
	// under (sessions/<name>/<RepoName> on the instance) -- the session's
	// own repo, not necessarily the instance's. One instance can host
	// sessions started from different repos via --name, and each keeps the
	// remote directory name its own repo derived at creation, so this must
	// travel with the session rather than be assumed equal to the record's
	// name at every later lookup.
	//
	// Empty means bivouac never recorded one, which is every session that
	// predates this field; callers fall back to the record's name, which
	// is correct for exactly the case that fallback was the only behavior.
	RepoName string `json:"repo_name,omitempty"`
	// HerdrMachineID is the saved-machine profile `bivouac herdr`
	// registered for this session, if any.
	//
	// Recorded rather than inferred. A herdr profile carries no owner
	// field, so matching on label or target could be defeated by a rename
	// or by a profile the user added themselves -- and teardown would then
	// delete something that was never bivouac's. Holding the id makes
	// cleanup exact: it removes this profile and can see no other.
	//
	// Empty means bivouac never registered one, which is also every
	// session that predates this field.
	HerdrMachineID   string            `json:"herdr_machine_id,omitempty"`
	Generation       string            `json:"generation,omitempty"`
	AttachmentsKnown bool              `json:"attachments_known,omitempty"`
	Attachments      []HerdrAttachment `json:"attachments,omitempty"`
	Retirement       *Retirement       `json:"retirement,omitempty"`
	// Beads is where the session's issue database stood when it started.
	// Nil means none was recorded -- beads was never wired, the read
	// failed, or the session predates issue reporting -- and every reader
	// treats all three the same way: nothing to report, nothing to wait on.
	Beads *SessionBeads `json:"beads,omitempty"`
}

// SessionBeads is the "before" every session issue report diffs against.
//
// StartCommit is the instance database's own commit, not the Mac's: the
// instance is where every later diff runs, so its commit is the only one
// guaranteed to resolve there. Planned is captured now rather than derived
// later because an issue's children can change during the session, and
// "planned versus discovered" is only meaningful against the tree as it
// stood at the start.
type SessionBeads struct {
	StartCommit string   `json:"start_commit"`
	Issue       string   `json:"issue,omitempty"`
	Planned     []string `json:"planned,omitempty"`
}

// RepoNameOr returns the identity the session's remote directory was
// seeded under, falling back to instanceName for a session that predates
// RepoName -- back then repoName was always the instance's own name, so
// that fallback is exact rather than a guess.
func (s Session) RepoNameOr(instanceName string) string {
	if s.RepoName != "" {
		return s.RepoName
	}
	return instanceName
}

// FindSession returns the session called name, if the instance has one.
func (r Record) FindSession(name string) (Session, bool) {
	for _, s := range r.Sessions {
		if s.Name == name {
			return s, true
		}
	}
	return Session{}, false
}

// PutSession adds a session, replacing any existing one with the same name.
// Replacing rather than appending matters for the retry path: starting the
// same session twice must not leave two entries whose bases disagree.
func (r *Record) PutSession(s Session) {
	for i := range r.Sessions {
		if r.Sessions[i].Name == s.Name {
			r.Sessions[i] = s
			return
		}
	}
	r.Sessions = append(r.Sessions, s)
}

// RemoveSession drops a session by name. Absent is not an error -- merge and
// delete both call this and neither should care whether it ran twice.
//
// A fresh slice rather than filtering into r.Sessions[:0]: callers hold Record
// by value, so reusing the backing array rewrites the caller's own elements
// underneath a header still reporting the old length.
func (r *Record) RemoveSession(name string) {
	out := make([]Session, 0, len(r.Sessions))
	for _, s := range r.Sessions {
		if s.Name != name {
			out = append(out, s)
		}
	}
	r.Sessions = out
}

// Store is a JSON-backed key-value store of instance Records, keyed by
// name, at the XDG state path.
type Store struct {
	path string
}

// Open resolves the state file path without requiring the file to exist
// yet. See internal/xdg for the rule.
func Open() (*Store, error) {
	path, err := xdg.Path(xdg.State, "state.json")
	if err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

// List returns every stored Record, sorted by name. An empty slice (not
// an error) is returned if the state file doesn't exist yet.
func (s *Store) List() ([]Record, error) {
	release, err := s.lockState(context.Background(), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()
	records, err := s.allUnlocked()
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(records))
	for _, r := range records {
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Get looks up a single Record by name.
func (s *Store) Get(name string) (Record, bool, error) {
	release, err := s.lockState(context.Background(), false)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = release() }()
	records, err := s.allUnlocked()
	if err != nil {
		return Record{}, false, err
	}
	r, ok := records[name]
	return r, ok, nil
}

// Put creates or replaces the Record for r.Name.
func (s *Store) Put(r Record) error {
	release, err := s.lockState(context.Background(), true)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	records, err := s.allUnlocked()
	if err != nil {
		return err
	}
	records[r.Name] = r
	return s.saveUnlocked(records)
}

// Update reloads and mutates one record under the short state-file lock.
func (s *Store) Update(instance string, mutate func(*Record) error) error {
	release, err := s.lockState(context.Background(), true)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	records, err := s.allUnlocked()
	if err != nil {
		return err
	}
	record, ok := records[instance]
	if !ok {
		return fmt.Errorf("instance %s not found", instance)
	}
	if err := mutate(&record); err != nil {
		return err
	}
	records[instance] = record
	return s.saveUnlocked(records)
}

// CompleteRetirement records success and removes exactly the generation that
// supplied its evidence in one state transaction.
func (s *Store) CompleteRetirement(instance, generation string, receipt RetirementReceipt) error {
	return s.Update(instance, func(record *Record) error {
		index := -1
		for i := range record.Sessions {
			if record.Sessions[i].Generation == generation {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("session generation %s is no longer active on %s", generation, instance)
		}
		sess := record.Sessions[index]
		if receipt.Generation != generation || receipt.Session != sess.Name {
			return fmt.Errorf("retirement receipt does not match active generation %s", generation)
		}
		record.Sessions = append(append([]Session(nil), record.Sessions[:index]...), record.Sessions[index+1:]...)
		kept := make([]RetirementReceipt, 0, len(record.RetirementReceipts)+1)
		for _, existing := range record.RetirementReceipts {
			if existing.Session != receipt.Session || existing.LocalRepo != receipt.LocalRepo {
				kept = append(kept, existing)
			}
		}
		record.RetirementReceipts = append(kept, receipt)
		return nil
	})
}

// Delete removes the Record for name, if present.
func (s *Store) Delete(name string) error {
	release, err := s.lockState(context.Background(), true)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	records, err := s.allUnlocked()
	if err != nil {
		return err
	}
	delete(records, name)
	return s.saveUnlocked(records)
}

func (s *Store) allUnlocked() (map[string]Record, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.path, err)
	}
	for _, record := range records {
		if err := validateRecordLifecycle(record); err != nil {
			return nil, fmt.Errorf("corrupt state file %s: %w", s.path, err)
		}
	}
	if records == nil {
		records = map[string]Record{}
	}
	return records, nil
}

func (s *Store) saveUnlocked(records map[string]Record) error {
	for _, record := range records {
		if err := validateRecordLifecycle(record); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, data, 0o600)
}
