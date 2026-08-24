package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ActiveSnapshot is the current managed memory view used to consolidate a new
// extraction. Entries are the complete active index; Records contains the
// details that could be loaded and validated from the immutable record store.
type ActiveSnapshot struct {
	Entries []ManagedEntry
	Records []*Record
}

// ReviewSessionID is the synthetic session key an index review commits under.
// Real session IDs are 17-digit timestamps, so this cannot collide with one.
//
// Reusing the per-session checkpoint for reviews is what keeps them from
// repeating: the "fingerprint" is the index state itself, so an unchanged index
// is already covered and a review that changed nothing will not run again until
// the index moves.
const ReviewSessionID = "index-review"

// IndexFingerprint is a deterministic digest of the active index identity. It
// changes when an entry is added, retired, or has its summary rewritten, which
// is exactly when another review pass could reach a different conclusion.
func (s *ActiveSnapshot) IndexFingerprint() string {
	if s == nil {
		return "empty"
	}
	ids := make([]string, 0, len(s.Entries))
	for _, e := range s.Entries {
		ids = append(ids, e.ID+"\x00"+e.Summary)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x1e")))
	return hex.EncodeToString(sum[:])[:hashHexLen]
}

// ActiveSnapshot loads the current managed index and its record details.
// Missing or malformed record files are omitted from Records and reported as
// warnings while their index entries remain available for duplicate avoidance.
// MEMORY.md parse failures remain fatal because there is no trustworthy active
// view to consolidate against.
func (m *Manager) ActiveSnapshot() (*ActiveSnapshot, []string, error) {
	idx, err := m.LoadIndex()
	if err != nil {
		return nil, nil, err
	}
	return m.activeSnapshotForIndex(idx)
}

func (m *Manager) activeSnapshotForIndex(idx *MemoryIndex) (*ActiveSnapshot, []string, error) {
	snapshot := &ActiveSnapshot{Entries: append([]ManagedEntry(nil), idx.Managed...)}
	var warnings []string
	for _, entry := range idx.Managed {
		if !ValidateRecordID(entry.ID) {
			warnings = append(warnings, fmt.Sprintf("invalid active record id %q", entry.ID))
			continue
		}
		rec, err := loadRecord(recordPath(m.layout.RecordsDir, entry.ID))
		if err != nil {
			if os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("active record %s is missing", entry.ID))
			} else {
				warnings = append(warnings, fmt.Sprintf("active record %s cannot be loaded: %v", entry.ID, err))
			}
			continue
		}
		if rec.ID != entry.ID {
			warnings = append(warnings, fmt.Sprintf("active record %s has mismatched id %q", entry.ID, rec.ID))
			continue
		}
		snapshot.Records = append(snapshot.Records, rec)
	}
	return snapshot, warnings, nil
}
