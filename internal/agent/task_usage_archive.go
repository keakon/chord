package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/config"
)

// taskUsageArchiveContextCheckInterval bounds how many archived records a scan
// decodes between context checks, so a cancelled Tasks query stops promptly
// without adding a check to every record.
const taskUsageArchiveContextCheckInterval = 256

// taskUsageArchiveMarker identifies one snapshot of a session's archive file.
// The archive is append-only and only written under taskRegistryPersistMu, so
// the size captured under that lock always lands on a record boundary; reading
// exactly that prefix later yields the same complete records even if the file
// has grown. Epoch orders concurrent scans from different sessions.
type taskUsageArchiveMarker struct {
	epoch      uint64
	sessionDir string
	exists     bool
	size       int64
	modTime    time.Time
}

func (m taskUsageArchiveMarker) equal(other taskUsageArchiveMarker) bool {
	return m.epoch == other.epoch &&
		m.sessionDir == other.sessionDir &&
		m.exists == other.exists &&
		m.size == other.size &&
		m.modTime.Equal(other.modTime)
}

// taskUsageArchiveRow is the projection the Tasks report needs from an archived
// durable task: display metadata plus the external CLI's latest cumulative
// report. Full records are never retained in the cache.
type taskUsageArchiveRow struct {
	TaskID            string
	AgentDefName      string
	TaskDesc          string
	Backend           string
	Reported          *AgentUsage
	Attempt           uint64
	LifecycleRevision uint64
	order             int64
}

// taskUsageCache holds the projection for the current session's archive
// snapshot. The mutex only guards the snapshot swap; file IO and decoding run
// outside it.
type taskUsageCache struct {
	mu     sync.Mutex
	marker taskUsageArchiveMarker
	rows   map[string]taskUsageArchiveRow
	loaded bool
}

func (c *taskUsageCache) lookup(marker taskUsageArchiveMarker) (map[string]taskUsageArchiveRow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || !c.marker.equal(marker) {
		return nil, false
	}
	return c.rows, true
}

// publish installs a completed scan unless a newer snapshot is already cached.
// The rows map is treated as immutable after publication.
func (c *taskUsageCache) publish(marker taskUsageArchiveMarker, rows map[string]taskUsageArchiveRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		if marker.epoch < c.marker.epoch {
			return
		}
		if marker.epoch == c.marker.epoch {
			if marker.sessionDir != c.marker.sessionDir {
				return
			}
			if c.marker.exists && marker.exists {
				if marker.size < c.marker.size {
					return
				}
				if marker.size == c.marker.size && marker.modTime.Equal(c.marker.modTime) {
					return
				}
			}
			if c.marker.exists && !marker.exists {
				return
			}
		}
	}
	c.marker = marker
	c.rows = rows
	c.loaded = true
}

// scanTaskUsageArchive decodes the captured archive prefix and keeps the newest
// record per task. Records repeat when the same task is archived more than
// once; a higher attempt wins, then a higher lifecycle revision, and an exact
// tie keeps the later archive entry. A newer record without a valid usage
// report keeps the latest positive report already seen, since a nil/zero usage
// is not an observation. The returned rows may be partial when the decode fails
// partway through; callers must not cache such a scan.
func scanTaskUsageArchive(ctx context.Context, sessionDir string, marker taskUsageArchiveMarker) (map[string]taskUsageArchiveRow, error) {
	rows := make(map[string]taskUsageArchiveRow)
	if !marker.exists {
		return rows, nil
	}
	path := durableTaskArchivePath(sessionDir)
	if path == "" {
		return rows, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rows, nil
		}
		return nil, fmt.Errorf("open task archive: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(bufio.NewReaderSize(io.LimitReader(f, marker.size), 64*1024))
	for seq := int64(0); ; seq++ {
		if seq%taskUsageArchiveContextCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var entry archivedTaskRecord
		if err := dec.Decode(&entry); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return rows, fmt.Errorf("decode task archive: %w", err)
		}
		row, ok := taskUsageArchiveRowFromRecord(entry.Task)
		if !ok {
			continue
		}
		row.order = seq
		if existing, ok := rows[row.TaskID]; ok {
			if !archiveRowNewer(row, existing) {
				continue
			}
			// Lifecycle metadata is newest-wins, but a nil/zero usage is not an
			// observation (see observeResultUsage). A newer archived record with
			// no valid report must not erase the latest positive report already
			// attributed to this task, or an external-only task would lose its
			// cost and disappear from the Tasks view entirely.
			if row.Reported == nil {
				row.Reported = existing.Reported
			}
		}
		rows[row.TaskID] = row
	}
	return rows, nil
}

func taskUsageArchiveRowFromRecord(rec *DurableTaskRecord) (taskUsageArchiveRow, bool) {
	if rec == nil {
		return taskUsageArchiveRow{}, false
	}
	taskID := strings.TrimSpace(rec.TaskID)
	if taskID == "" {
		return taskUsageArchiveRow{}, false
	}
	row := taskUsageArchiveRow{
		TaskID:            taskID,
		AgentDefName:      strings.TrimSpace(rec.AgentDefName),
		TaskDesc:          strings.TrimSpace(rec.TaskDesc),
		Attempt:           max(rec.Attempt, 1),
		LifecycleRevision: rec.LifecycleRevision,
	}
	if rec.ExternalSession != nil {
		row.Backend = strings.TrimSpace(rec.ExternalSession.Backend)
		if row.Backend == "" {
			row.Backend = config.BackendKindClaudeCode
		}
		if rec.Usage != nil && !rec.Usage.Zero() {
			usage := *rec.Usage
			row.Reported = &usage
		}
	}
	return row, true
}

func archiveRowNewer(candidate, existing taskUsageArchiveRow) bool {
	if candidate.Attempt != existing.Attempt {
		return candidate.Attempt > existing.Attempt
	}
	if candidate.LifecycleRevision != existing.LifecycleRevision {
		return candidate.LifecycleRevision > existing.LifecycleRevision
	}
	return candidate.order > existing.order
}
