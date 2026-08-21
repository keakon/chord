package memory

import (
	"fmt"
	"os"
)

// ActiveSnapshot is the current managed memory view used to consolidate a new
// extraction. Entries are the complete active index; Records contains the
// details that could be loaded and validated from the immutable record store.
type ActiveSnapshot struct {
	Entries []ManagedEntry
	Records []*Record
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
