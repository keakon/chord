package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/atomicfile"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/jsonx"
)

// storeVersion guards the on-disk shape. Bump it when the record meaning
// changes in a way old readers must not interpret.
const storeVersion = 1

// ProjectTrust is the persisted authorization record of one project root.
// Inputs keeps the exact declaration file set the user authorized, so
// runtime re-checks (worktree switches, dynamic skill loads) can compare
// against what was approved instead of only the digest.
type ProjectTrust struct {
	Fingerprint string   `json:"fingerprint,omitempty"`
	GrantedAtNs int64    `json:"granted_at_ns,omitzero"`
	Inputs      InputSet `json:"inputs,omitempty"`
}

// Store is the on-disk authorization store. Records are keyed by the
// canonical project root path.
type Store struct {
	Version  int                     `json:"version,omitzero"`
	Projects map[string]ProjectTrust `json:"projects,omitempty"`
}

// Path returns the trust store location inside the state directory.
func Path(stateDir string) string {
	return filepath.Join(stateDir, "project-trust.json")
}

// Load reads the store. A missing file yields an empty store; a store that
// cannot be parsed is an error — a damaged store must never read as
// authorized.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Version: storeVersion, Projects: map[string]ProjectTrust{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read trust store %s: %w", path, err)
	}
	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse trust store %s: %w", path, err)
	}
	if store.Projects == nil {
		store.Projects = map[string]ProjectTrust{}
	}
	return &store, nil
}

// Lookup returns the record for a canonical project root, if present.
func (s *Store) Lookup(projectRoot string) (ProjectTrust, bool) {
	record, ok := s.Projects[projectRoot]
	return record, ok
}

// Authorize records the user's consent for a project root under a
// cross-process lock. It overwrites any previous record for the root:
// re-confirmation always means the latest authorized state.
func Authorize(storePath, projectRoot string, inputs InputSet, now time.Time) error {
	inputs = inputs.Normalize()
	if projectRoot == "" || inputs.Empty() {
		return fmt.Errorf("authorize requires a project root and inputs")
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		return fmt.Errorf("create trust store directory: %w", err)
	}
	lock, err := config.LockConfigMutation(storePath)
	if err != nil {
		return fmt.Errorf("lock trust store: %w", err)
	}
	defer func() { _ = lock.Close() }()

	store, err := Load(storePath)
	if err != nil {
		return err
	}
	store.Version = storeVersion
	store.Projects[projectRoot] = ProjectTrust{
		Fingerprint: inputs.Fingerprint(),
		GrantedAtNs: now.UnixNano(),
		Inputs:      inputs,
	}
	data, err := jsonx.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trust store: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Replace(storePath, data, 0o600); err != nil {
		return fmt.Errorf("write trust store: %w", err)
	}
	return nil
}

// Revoke removes the record for a project root and reports whether a
// record existed. A failed write keeps the old record so authorization
// state never silently disappears; the error is returned for reporting.
func Revoke(storePath, projectRoot string) (bool, error) {
	lock, err := config.LockConfigMutation(storePath)
	if err != nil {
		return false, fmt.Errorf("lock trust store: %w", err)
	}
	defer func() { _ = lock.Close() }()

	store, err := Load(storePath)
	if err != nil {
		return false, err
	}
	_, existed := store.Projects[projectRoot]
	if !existed {
		return false, nil
	}
	delete(store.Projects, projectRoot)
	data, err := jsonx.MarshalIndent(store, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode trust store: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Replace(storePath, data, 0o600); err != nil {
		return true, fmt.Errorf("write trust store: %w", err)
	}
	return true, nil
}
