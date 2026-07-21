package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// readValidity classifies a successful read result relative to the rest of the
// conversation. Invalidated means the file was modified by a later successful
// edit/patch/write/delete, so the read's content is no longer trustworthy.
// Superseded means a later read of the same file covers this read's whole
// range, so a fresher copy of the same content exists later in context.
// A read that is neither is the model's only current view of that content.
type readValidity struct {
	Invalidated bool
	Superseded  bool
}

type readValidityRecord struct {
	index         int
	keys          validityKeySet
	start         int
	end           int
	canonicalPath bool
}

type validityKeySet struct {
	first string
	rest  []string
}

func (s *validityKeySet) add(raw string) {
	key := reductionNormalizePath(raw)
	if key == "" || key == s.first || slices.Contains(s.rest, key) {
		return
	}
	if s.first == "" {
		s.first = key
		return
	}
	s.rest = append(s.rest, key)
}

func (s validityKeySet) len() int {
	if s.first == "" {
		return 0
	}
	return 1 + len(s.rest)
}

func (s validityKeySet) at(index int) string {
	if index == 0 {
		return s.first
	}
	return s.rest[index-1]
}

type editValidityRecord struct {
	index     int
	start     int
	end       int
	lineDelta int
	canonical bool
}

// analyzeReadValidity maps tool-result message indices of successful reads to
// their validity. Only in-conversation evidence is used (later edit calls and
// later reads); file modifications made outside tracked tools are not visible
// and leave a read conservatively classified as still valid.
func analyzeReadValidity(messages []message.Message, callMeta map[string]toolCallMeta) map[int]readValidity {
	var reads []readValidityRecord
	editsByKey := make(map[string][]editValidityRecord)
	firstReadByKey := make(map[string]int) // key -> first read index + 1
	var repeatedReadsByKey map[string][]int

	for i := range messages {
		msg := &messages[i]
		if msg.Role != message.RoleTool || isToolResultErrorStatus(msg.ToolStatus) {
			continue
		}
		meta := callMeta[msg.ToolCallID]
		switch strings.TrimSpace(meta.Name) {
		case tools.NameRead:
			record, ok := buildReadValidityRecord(i, &meta, msg)
			callMeta[msg.ToolCallID] = meta
			if !ok {
				continue
			}
			readIdx := len(reads)
			reads = append(reads, record)
			for keyIndex := 0; keyIndex < record.keys.len(); keyIndex++ {
				key := record.keys.at(keyIndex)
				if indices := repeatedReadsByKey[key]; len(indices) > 0 {
					repeatedReadsByKey[key] = append(indices, readIdx)
				} else if firstPlusOne := firstReadByKey[key]; firstPlusOne > 0 {
					if repeatedReadsByKey == nil {
						repeatedReadsByKey = make(map[string][]int)
					}
					repeatedReadsByKey[key] = []int{firstPlusOne - 1, readIdx}
				} else {
					firstReadByKey[key] = readIdx + 1
				}
			}
		case tools.NameEdit, tools.NamePatch, tools.NameWrite, tools.NameDelete:
			for key, edit := range editTargetRecords(i, meta.Args, msg.FileState) {
				editsByKey[key] = append(editsByKey[key], edit)
			}
		}
	}
	if len(reads) == 0 {
		return nil
	}
	if len(editsByKey) == 0 && len(repeatedReadsByKey) == 0 {
		return nil
	}
	supersededReads := analyzeSupersededReads(reads, repeatedReadsByKey)

	// editKeys supports suffix matching for invalidation: within one session
	// the same file is often read via an absolute path but edited via a
	// relative one (or vice versa). A missed match here would keep an edited
	// file's old read marked as still current, which is worse than the rare
	// false positive of two files sharing a full path suffix.
	var editKeysBySuffix map[string][]string
	if len(editsByKey) > 0 {
		editKeysBySuffix = make(map[string][]string, len(editsByKey))
		for key := range editsByKey {
			forEachPathSuffix(key, func(suffix string) {
				editKeysBySuffix[suffix] = append(editKeysBySuffix[suffix], key)
			})
		}
	}

	var out map[int]readValidity
	for readIdx, record := range reads {
		validity := readValidity{Superseded: supersededReads[readIdx]}
		for keyIndex := 0; keyIndex < record.keys.len(); keyIndex++ {
			key := record.keys.at(keyIndex)
			if editedAfter(editsByKey, key, record) {
				validity.Invalidated = true
			}
		}
		if !validity.Invalidated {
			for keyIndex := 0; keyIndex < record.keys.len(); keyIndex++ {
				key := record.keys.at(keyIndex)
				forEachPathSuffix(key, func(suffix string) {
					if validity.Invalidated {
						return
					}
					for _, editKey := range editKeysBySuffix[suffix] {
						if editKey != key && pathSuffixMatch(editKey, key) && editedAfterSuffixFallback(editsByKey, editKey, record) {
							validity.Invalidated = true
							return
						}
					}
				})
				if validity.Invalidated {
					break
				}
			}
		}
		if validity.Invalidated || validity.Superseded {
			if out == nil {
				out = make(map[int]readValidity)
			}
			out[record.index] = validity
		}
	}
	return out
}

// analyzeSupersededReads reports reads wholly covered by a later read of the
// same path. For each path it scans reads newest-first while a Fenwick tree
// stores the maximum end line seen for every start-line prefix. This preserves
// the requirement that one later read covers the whole range and avoids the
// quadratic scan when a long session repeatedly reads the same file.
func analyzeSupersededReads(reads []readValidityRecord, readsByKey map[string][]int) map[int]bool {
	var superseded map[int]bool
	for _, readIndices := range readsByKey {
		starts := make([]int, len(readIndices))
		for i, readIdx := range readIndices {
			starts[i] = reads[readIdx].start
		}
		slices.Sort(starts)
		starts = slices.Compact(starts)
		maxEnds := make([]int, len(starts)+1)
		for _, readIdx := range slices.Backward(readIndices) {
			record := reads[readIdx]
			pos, _ := slices.BinarySearch(starts, record.start)
			if fenwickPrefixMax(maxEnds, pos+1) >= record.end {
				if superseded == nil {
					superseded = make(map[int]bool)
				}
				superseded[readIdx] = true
			}
			fenwickUpdateMax(maxEnds, pos+1, record.end)
		}
	}
	return superseded
}

func fenwickPrefixMax(tree []int, pos int) int {
	var maximum int
	for pos > 0 {
		maximum = max(maximum, tree[pos])
		pos -= pos & -pos
	}
	return maximum
}

func fenwickUpdateMax(tree []int, pos, value int) {
	for pos < len(tree) {
		tree[pos] = max(tree[pos], value)
		pos += pos & -pos
	}
}

func editedAfter(editsByKey map[string][]editValidityRecord, key string, read readValidityRecord) bool {
	return editedAfterMatching(editsByKey[key], read, false)
}

func editedAfterSuffixFallback(editsByKey map[string][]editValidityRecord, key string, read readValidityRecord) bool {
	return editedAfterMatching(editsByKey[key], read, read.canonicalPath)
}

func editedAfterMatching(edits []editValidityRecord, read readValidityRecord, skipCanonical bool) bool {
	laterEdits := 0
	hasLineShift := false
	for i := len(edits) - 1; i >= 0 && edits[i].index > read.index; i-- {
		edit := edits[i]
		if skipCanonical && edit.canonical {
			continue
		}
		laterEdits++
		hasLineShift = hasLineShift || edit.lineDelta != 0
		if edit.start == 0 || edit.end == 0 ||
			(edit.start <= read.end && edit.end >= read.start) ||
			(edit.lineDelta != 0 && edit.start < read.start) {
			return true
		}
	}
	// Changed ranges use each edit's immediate pre-edit coordinates. After a
	// line-count-changing edit, a later edit's coordinates cannot be compared
	// directly with an older read without replaying every delta; conservatively
	// invalidate instead of treating potentially shifted line numbers as current.
	if laterEdits > 1 && hasLineShift {
		return true
	}
	return false
}

// pathSuffixMatch reports whether the shorter of two normalized paths is a
// whole-segment suffix of the longer one, e.g. "internal/agent/main.go" and
// "/tmp/worktree/internal/agent/main.go".
func pathSuffixMatch(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasSuffix(b, "/"+a)
}

// forEachPathSuffix visits a path and each whole-segment suffix. Indexing edit
// paths by these suffixes avoids comparing every read with every edit while
// preserving pathSuffixMatch as the final guard against basename collisions.
func forEachPathSuffix(path string, visit func(string)) {
	if path == "" || visit == nil {
		return
	}
	visit(path)
	for start := 0; ; {
		rel := strings.IndexByte(path[start:], '/')
		if rel < 0 {
			return
		}
		start += rel + 1
		if start < len(path) {
			visit(path[start:])
		}
	}
}

func buildReadValidityRecord(index int, meta *toolCallMeta, msg *message.Message) (readValidityRecord, bool) {
	record := readValidityRecord{index: index}
	if msg.FileState != nil {
		for _, state := range msg.FileState.Reads {
			record.keys.add(state.Path)
			record.canonicalPath = record.canonicalPath || filepath.IsAbs(strings.TrimSpace(state.Path))
		}
	}
	displayed := parseDisplayedReadRange(msg.Content)
	var request readRequestSummary
	if record.keys.len() == 0 || !displayed.OK {
		request = meta.parsedReadRequest()
		record.keys.add(request.Path)
	}
	if record.keys.len() == 0 {
		return readValidityRecord{}, false
	}
	if displayed.OK && displayed.Start >= 1 && displayed.End >= displayed.Start {
		record.start = displayed.Start
		record.end = displayed.End
		return record, true
	}
	// Fallback to the requested window when the header is unparseable.
	start := request.Offset + 1
	limit := request.Limit
	if limit <= 0 {
		limit = tools.MaxOutputLines
	}
	record.start = start
	record.end = start + limit - 1
	return record, true
}

func editTargetRecords(index int, argsJSON string, state *message.ToolFileState) map[string]editValidityRecord {
	var keys validityKeySet
	var parsed struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err == nil {
		keys.add(parsed.Path)
		for _, path := range parsed.Paths {
			keys.add(path)
		}
	}
	records := make(map[string]editValidityRecord)
	localized := editValidityRecord{index: index}
	if state != nil {
		for _, write := range state.Writes {
			if localized.start == 0 && write.ChangedStart > 0 && write.ChangedEnd >= write.ChangedStart {
				localized.start, localized.end, localized.lineDelta = write.ChangedStart, write.ChangedEnd, write.LineDelta
			}
			key := reductionNormalizePath(write.Path)
			if key != "" {
				records[key] = editValidityRecord{index: index, start: write.ChangedStart, end: write.ChangedEnd, lineDelta: write.LineDelta, canonical: filepath.IsAbs(strings.TrimSpace(write.Path))}
			}
		}
		for _, deleted := range state.Deletes {
			key := reductionNormalizePath(deleted.Path)
			if key != "" {
				records[key] = editValidityRecord{index: index, canonical: filepath.IsAbs(strings.TrimSpace(deleted.Path))}
			}
		}
	}
	for keyIndex := 0; keyIndex < keys.len(); keyIndex++ {
		key := keys.at(keyIndex)
		if _, ok := records[key]; !ok {
			records[key] = localized
		}
	}
	return records
}

// reductionNormalizePath canonicalizes a tool-call path purely lexically so
// the same spelling always produces the same key. Absolute and relative
// spellings of one file intentionally stay distinct: resolving against the
// filesystem per request would be costly, and a missed match only means a read
// is retained longer, never reduced wrongly.
func reductionNormalizePath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." {
		return ""
	}
	return path
}
