package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Record is one instance's state: which provider created it, its VM and
// network details, and the PIDs of its background sync/tunnel processes.
type Record struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	VMID            string `json:"vm_id"`
	IP              string `json:"ip"`
	Region          string `json:"region"`
	Size            string `json:"size"`
	Template        string `json:"template"`
	User            string `json:"user"`
	RepoPath        string `json:"repo_path"`
	WatchPID        int    `json:"watch_pid"`
	TunnelPID       int    `json:"tunnel_pid"`
	TailscaleJoined bool   `json:"tailscale_joined"`
}

// Store is a JSON-backed key-value store of instance Records, keyed by
// name, at the XDG state path.
type Store struct {
	path string
}

// Open resolves the state file path ($XDG_STATE_HOME/cloudlab/state.json,
// else ~/.local/state/cloudlab/state.json on every OS) without requiring
// the file to exist yet.
func Open() (*Store, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return &Store{path: filepath.Join(dir, "cloudlab", "state.json")}, nil
}

// List returns every stored Record, sorted by name. An empty slice (not
// an error) is returned if the state file doesn't exist yet.
func (s *Store) List() ([]Record, error) {
	records, err := s.all()
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
	records, err := s.all()
	if err != nil {
		return Record{}, false, err
	}
	r, ok := records[name]
	return r, ok, nil
}

// Put creates or replaces the Record for r.Name.
func (s *Store) Put(r Record) error {
	records, err := s.all()
	if err != nil {
		return err
	}
	records[r.Name] = r
	return s.save(records)
}

// Delete removes the Record for name, if present.
func (s *Store) Delete(name string) error {
	records, err := s.all()
	if err != nil {
		return err
	}
	delete(records, name)
	return s.save(records)
}

func (s *Store) all() (map[string]Record, error) {
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
	if records == nil {
		records = map[string]Record{}
	}
	return records, nil
}

func (s *Store) save(records map[string]Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
