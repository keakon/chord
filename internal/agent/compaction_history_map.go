package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// historyMapTopicCap bounds how many distinct evidence titles a history
// archive advertises in the archived-history map, so the map stays a compact
// overview instead of growing into a second summary.
const historyMapTopicCap = 12

// evidenceItemTopics extracts the bounded content map for an archived history
// file: the highest-priority evidence titles that actually carry content.
// Titles that are generic scaffolding (completion/background bookkeeping, no
// actionable topic) are dropped so the map stays a map of what to look for,
// not a summary of the evidence machinery.
func evidenceItemTopics(items []evidenceItem) []string {
	prioritized := append([]evidenceItem(nil), items...)
	sort.SliceStable(prioritized, func(i, j int) bool {
		if prioritized[i].Priority != prioritized[j].Priority {
			return prioritized[i].Priority > prioritized[j].Priority
		}
		return prioritized[i].Sequence > prioritized[j].Sequence
	})
	seen := make(map[string]struct{}, len(prioritized))
	out := make([]string, 0, min(len(prioritized), historyMapTopicCap))
	for _, item := range prioritized {
		title := strings.TrimSpace(item.Title)
		if title == "" || strings.TrimSpace(item.Excerpt) == "" {
			continue
		}
		if _, duplicate := seen[title]; duplicate {
			continue
		}
		if isHistoryMapNoiseTitle(title) {
			continue
		}
		seen[title] = struct{}{}
		out = append(out, title)
		if len(out) >= historyMapTopicCap {
			break
		}
	}
	return out
}

func isHistoryMapNoiseTitle(title string) bool {
	switch title {
	case "SubAgent completion summary",
		"SubAgent requested main-agent help",
		"Latest Done rejection",
		"User correction / constraint",
		"Stated constraint",
		"Latest user request":
		return true
	default:
		return false
	}
}

// readCompactionHistoryMetas loads the topics-bearing meta for every known
// history file. Missing meta (an archive exported before topic persistence, or
// a meta write still in flight) is tolerated: the file still appears in the map
// with no topics rather than disappearing from the chain.
func readCompactionHistoryMetas(chainRefs []string) map[string]*compactionHistoryMeta {
	out := make(map[string]*compactionHistoryMeta, len(chainRefs))
	for _, ref := range chainRefs {
		metaPath := compactionHistoryMetaPath(ref)
		meta, err := readCompactionHistoryMeta(metaPath)
		if err == nil {
			out[filepath.Base(ref)] = &meta
		}
	}
	return out
}

// listCheckpointHistoryReferences lists the archived history files a new
// checkpoint may advertise, together with their metas.
//
// An archive whose .status.json still says pending_apply belongs to a draft
// that has not been applied: a cancelled or failed worker removes it, and that
// cleanup runs asynchronously, so an unfiltered listing can advertise a file
// the model would then fail to read. Such entries are dropped — except
// selfHistoryPath, the archive this very draft just exported, which is
// pending_apply by construction and is exactly what the checkpoint must point
// at. Archives with no readable meta are kept: a meta write still in flight
// must not make an otherwise valid archive disappear from the chain.
func listCheckpointHistoryReferences(sessionDir, selfHistoryPath string) ([]string, map[string]*compactionHistoryMeta, error) {
	refs, err := listHistoryReferences(sessionDir)
	if err != nil {
		return nil, nil, err
	}
	metas := readCompactionHistoryMetas(refs)
	self := ""
	if selfHistoryPath != "" {
		self = filepath.Base(selfHistoryPath)
	}
	kept := make([]string, 0, len(refs))
	for _, ref := range refs {
		base := filepath.Base(ref)
		if base != self {
			if meta, ok := metas[base]; ok && meta.Status == compactionHistoryPending {
				continue
			}
		}
		kept = append(kept, ref)
	}
	return kept, metas, nil
}

// formatHistoryMapLines renders the bounded "archived history map" inside the
// checkpoint wrapper: one line per archived history file, paired with its
// content topics. The model sees at a glance what each archive covers and
// can read the exact archive back by its stable relative address instead of
// guessing which file to open. Files without readable topics metadata fall
// back to the plain path line.
func formatHistoryMapLines(chainRefs []string, metas map[string]*compactionHistoryMeta) []string {
	// Bounded to the newest entries. The list gains a line per compaction and
	// never shrinks, so an old session spends a growing slice of every
	// checkpoint restating archives it will almost never reopen. The omitted
	// ones stay addressable because the note names the range it dropped: the
	// indices are handed out monotonically but are not contiguous, since a
	// cancelled or failed compaction removes its archive, so the range is read
	// off the dropped entries themselves rather than derived from the count.
	omitted := 0
	var dropped []string
	if len(chainRefs) > compactHistoryMapMaxEntries {
		omitted = len(chainRefs) - compactHistoryMapMaxEntries
		dropped = chainRefs[:omitted]
		chainRefs = chainRefs[omitted:]
	}
	lines := make([]string, 0, len(chainRefs)+1)
	if omitted == 1 {
		lines = append(lines, fmt.Sprintf("(1 earlier archive omitted: %s in the same directory)", filepath.Base(dropped[0])))
	} else if omitted > 1 {
		lines = append(lines, fmt.Sprintf("(%d earlier archives omitted, from %s to %s in the same directory)",
			omitted, filepath.Base(dropped[0]), filepath.Base(dropped[len(dropped)-1])))
	}
	for _, ref := range chainRefs {
		line := pathutil.AbbreviateHome(ref)
		if meta, ok := metas[filepath.Base(ref)]; ok && len(meta.Topics) > 0 {
			line += ": " + strings.Join(meta.Topics, ", ")
		}
		lines = append(lines, line)
	}
	return lines
}
