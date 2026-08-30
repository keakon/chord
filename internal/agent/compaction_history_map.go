package agent

import (
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

// formatHistoryMapLines renders the bounded "archived history map" inside the
// checkpoint wrapper: one line per archived history file, paired with its
// content topics. The model sees at a glance what each archive covers and
// can read the exact archive back by its stable relative address instead of
// guessing which file to open. Files without readable topics metadata fall
// back to the plain path line.
func formatHistoryMapLines(chainRefs []string, metas map[string]*compactionHistoryMeta) []string {
	lines := make([]string, 0, len(chainRefs))
	for _, ref := range chainRefs {
		line := pathutil.AbbreviateHome(ref)
		if meta, ok := metas[filepath.Base(ref)]; ok && len(meta.Topics) > 0 {
			line += ": " + strings.Join(meta.Topics, ", ")
		}
		lines = append(lines, line)
	}
	return lines
}
