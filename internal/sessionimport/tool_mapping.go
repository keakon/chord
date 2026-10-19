package sessionimport

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// Imported tool mapping turns a source agent's call into the Chord tool that
// actually performed the same work, or leaves it alone when none did. The
// mapping reads the arguments rather than the agent's vocabulary: what decides
// which Chord tool a call was is the shape of the values it recorded, not the
// name printed on the call.

// importedToolCandidates lists the Chord tools that could stand in for each
// source tool name, closest first. A tool that takes the source's values under
// its own names comes before one the values would have to be rewritten into,
// so a before/after replacement stays a replacement and only a genuine patch
// becomes a patch. A name with no entry, or whose entries all fail to read its
// arguments, stays the source call it was (see resolveImportedTool).
var importedToolCandidates = map[string][]string{
	// Commands.
	"bash":         {tools.NameShell},
	"shell":        {tools.NameShell},
	"exec":         {tools.NameShell},
	"exec_command": {tools.NameShell},
	// Reads.
	"read":      {tools.NameRead},
	"read_file": {tools.NameRead},
	"file_read": {tools.NameRead},
	// Writes.
	"write":      {tools.NameWrite},
	"write_file": {tools.NameWrite},
	"file_write": {tools.NameWrite},
	// Replacements and patches. The pair comes first because both sides name
	// the same work — swapping one block of text for another — while the
	// patch tools speak in hunks and only fit a call that really recorded one.
	"edit":        {tools.NameEdit, tools.NameApplyPatch},
	"update":      {tools.NameEdit, tools.NameApplyPatch},
	"edit_file":   {tools.NameEdit, tools.NameApplyPatch},
	"multiedit":   {tools.NameEdit, tools.NameApplyPatch},
	"apply_patch": {tools.NameApplyPatch, tools.NameEdit},
	// Deletions.
	"delete":      {tools.NameDelete},
	"remove":      {tools.NameDelete},
	"delete_file": {tools.NameDelete},
	"file_delete": {tools.NameDelete},
	"remove_file": {tools.NameDelete},
	// Searches.
	"grep":       {tools.NameGrep},
	"search":     {tools.NameGrep},
	"glob":       {tools.NameGlob},
	"list_files": {tools.NameGlob},
	// Questions.
	"askuserquestion": {tools.NameQuestion},
}

// resolveImportedTool maps a source tool call onto the Chord tool that did the
// same work, with arguments in that tool's own shape.
//
// Candidates are tried in closeness order and the first one that can read the
// recorded arguments wins, so the mapping follows what the call did. Nothing
// here invented out of partial arguments is better than the original: when no
// candidate reads them, ok is false and the caller keeps the source name and
// arguments together as the historical record.
func resolveImportedTool(sourceName string, rawArgs json.RawMessage) (chordName string, args json.RawMessage, ok bool) {
	candidates := importedToolCandidates[normalizeImportedToolName(sourceName)]
	if len(candidates) == 0 || len(rawArgs) == 0 {
		return "", nil, false
	}
	parsed, ok := parseImportedToolArgs(rawArgs)
	if !ok {
		return "", nil, false
	}
	for _, candidate := range candidates {
		if normalized := normalizeImportedToolArgs(candidate, parsed); normalized != nil {
			return candidate, normalized, true
		}
	}
	return "", nil, false
}

// importedToolClosestIs reports whether a source tool's closest Chord
// candidate is target, letting a caller adapt argument units before the
// arguments are normalized.
func importedToolClosestIs(sourceName, target string) bool {
	candidates := importedToolCandidates[normalizeImportedToolName(sourceName)]
	return len(candidates) > 0 && candidates[0] == target
}

// importedShellTimeoutToSeconds rewrites the millisecond `timeout` that
// Claude's Bash and OpenCode's bash record into the seconds the shared
// argument normalizer expects. Feeding the source value straight through
// would be multiplied by a thousand again, leaving the imported call a
// deadline it never ran with.
func importedShellTimeoutToSeconds(args map[string]any) map[string]any {
	ms := codexPickFloat(args, "timeout")
	if ms <= 0 {
		return args
	}
	out := make(map[string]any, len(args))
	maps.Copy(out, args)
	out["timeout"] = ms / 1000
	return out
}

func normalizeImportedToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// parseImportedToolArgs reads the arguments of a source tool call. The Codex
// responses API sends them as a JSON string rather than an object, so that
// spelling is unwrapped too; anything else is left to the caller as a call it
// cannot read rather than rewritten into a guess.
func parseImportedToolArgs(raw json.RawMessage) (map[string]any, bool) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err == nil {
		return args, true
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, false
	}
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		return nil, false
	}
	return args, true
}

// normalizeImportedToolArgs reads a source call's recorded arguments into the
// argument shape of the Chord tool it was mapped to. All three importers come
// through here, so each normalizer accepts the spellings every source uses
// (`cmd`/`command`, `file_path`/`filePath`) instead of one per importer.
func normalizeImportedToolArgs(chordName string, args map[string]any) json.RawMessage {
	switch chordName {
	case tools.NameEdit:
		return normalizeReplaceEditArgs(args)
	case tools.NameApplyPatch:
		return normalizeApplyPatchArgs(args)
	case tools.NameShell:
		return normalizeImportedShellArgs(args)
	case tools.NameRead:
		return normalizeImportedReadArgs(args)
	case tools.NameWrite:
		return normalizeImportedWriteArgs(args)
	case tools.NameDelete:
		return normalizeImportedDeleteArgs(args)
	case tools.NameGrep:
		return normalizeImportedGrepArgs(args)
	case tools.NameGlob:
		return normalizeImportedGlobArgs(args)
	case tools.NameQuestion:
		return normalizeImportedQuestionArgs(args)
	default:
		return nil
	}
}

// importedQuestionIDMaxBytes is Chord's bound on a question option ID, which a
// source label has to fit when the label becomes the ID.
const importedQuestionIDMaxBytes = 128

// normalizeImportedQuestionArgs reads a source "ask the user" call into Chord's
// question arguments. Both record the same act — offering labelled choices and
// waiting for one — so the values carry over under Chord's names: the flag
// becomes multiple and an option keeps its label and description.
//
// Chord names an option by ID while the source named it by its label, and the
// label is what the user was shown, so the label becomes the ID rather than a
// synthesized placeholder. A call whose labels cannot identify their options
// (blank, repeated, or longer than an ID may be) has no faithful reading and
// stays the source call it was.
func normalizeImportedQuestionArgs(args map[string]any) json.RawMessage {
	items, ok := importedQuestionItems(args["questions"])
	if !ok || len(items) == 0 {
		return nil
	}
	normalized := make([]map[string]any, 0, len(items))
	for _, item := range items {
		question := strings.TrimSpace(codexPickString(item, "question"))
		header := strings.TrimSpace(codexPickString(item, "header"))
		if question == "" || header == "" {
			return nil
		}
		entry := map[string]any{"question": question, "header": header}
		if multiple := pickImportedBool(item, "multiSelect", "multi_select", "multiple"); multiple != nil && *multiple {
			entry["multiple"] = true
		}
		options, ok := importedQuestionOptions(item["options"])
		if !ok {
			return nil
		}
		if len(options) > 0 {
			entry["options"] = options
		}
		normalized = append(normalized, entry)
	}
	raw, err := json.Marshal(map[string]any{"questions": normalized})
	if err != nil {
		return nil
	}
	// A reading Chord's own parser rejects is not the question the source
	// asked, so the call stays as recorded rather than imported as a call the
	// tool would have refused.
	if _, err := tools.DecodeQuestionArgs(raw); err != nil {
		return nil
	}
	return raw
}

// importedQuestionItems reads the recorded questions, accepting the single
// object spelling a source may have used for one question.
func importedQuestionItems(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			item, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			items = append(items, item)
		}
		return items, true
	case map[string]any:
		return []map[string]any{typed}, true
	default:
		return nil, false
	}
}

// importedQuestionOptions reads the recorded choices, keying each one by its
// label. An absent list is a free-form question Chord also allows.
func importedQuestionOptions(value any) ([]map[string]any, bool) {
	if value == nil {
		return nil, true
	}
	rawOptions, ok := value.([]any)
	if !ok {
		return nil, false
	}
	if len(rawOptions) == 0 {
		return nil, true
	}
	options := make([]map[string]any, 0, len(rawOptions))
	seen := make(map[string]bool, len(rawOptions))
	for _, rawOption := range rawOptions {
		option, ok := rawOption.(map[string]any)
		if !ok {
			return nil, false
		}
		label := strings.TrimSpace(codexPickString(option, "label"))
		if label == "" || len(label) > importedQuestionIDMaxBytes || seen[label] {
			return nil, false
		}
		seen[label] = true
		entry := map[string]any{"id": label, "label": label}
		if description := codexPickString(option, "description"); description != "" {
			entry["description"] = description
		}
		options = append(options, entry)
	}
	return options, true
}

// normalizeReplaceEditArgs reads a source edit recorded as a before/after pair
// into Chord's edit arguments. The two halves carry over under Chord's names
// and nothing is added: every spelling a source uses names the same act of
// replacing one block of text, so reading all of them is what lets the record
// import as the edit it was. A source that already grouped several commands
// into one call maps onto edit's own batch instead of being flattened.
//
// The location is the pair itself, so a call missing a path or the text it
// replaced cannot become an edit: it has no location to show, and the caller
// falls through to the next candidate or keeps the record as it was issued.
func normalizeReplaceEditArgs(args map[string]any) json.RawMessage {
	path := codexPickString(args, "path", "file_path", "filePath", "file")
	if path == "" {
		return nil
	}
	if batch := normalizeReplaceEditBatch(args); batch != nil {
		result := map[string]any{"path": path, "edits": batch}
		b, _ := json.Marshal(result)
		return b
	}
	oldText := codexPickString(args, "old_string", "oldString", "old", "old_text")
	if oldText == "" {
		return nil
	}
	newText := codexPickString(args, "new_string", "newString", "new", "new_text")
	result := map[string]any{"path": path, "old_string": oldText, "new_string": newText}
	if replaceAll := pickImportedBool(args, "replace_all", "replaceAll"); replaceAll != nil {
		result["replace_all"] = *replaceAll
	}
	b, _ := json.Marshal(result)
	return b
}

// normalizeReplaceEditBatch reads a source list of replacements into the
// entries edit batches. A single entry that lost either side makes the whole
// list unusable rather than a partial edit Chord never issued, so nil is
// returned and the single-pair reading gets its turn.
func normalizeReplaceEditBatch(args map[string]any) []map[string]any {
	raw, ok := args["edits"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		oldText := codexPickString(entry, "old_string", "oldString", "old", "old_text")
		if oldText == "" {
			return nil
		}
		newText := codexPickString(entry, "new_string", "newString", "new", "new_text")
		entries = append(entries, map[string]any{"old_string": oldText, "new_string": newText})
	}
	return entries
}

// normalizeApplyPatchArgs reads a source edit recorded as a patch into Chord's
// apply_patch argument. An envelope the source already spoke in carries over
// verbatim, and a bare patch body is wrapped so it is still one envelope. A
// before/after pair is deliberately not read here: it belongs to another tool,
// and repackaging it would invent a patch the source agent never wrote.
func normalizeApplyPatchArgs(args map[string]any) json.RawMessage {
	patch := codexPickString(args, "patch", "input")
	if patch == "" {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(patch), "*** Begin Patch") {
		result := map[string]any{"patch": patch}
		b, _ := json.Marshal(result)
		return b
	}
	path := codexPickString(args, "path", "file_path", "filePath", "file")
	if path == "" {
		return nil
	}
	result := map[string]any{"patch": buildImportedApplyPatchEnvelope(path, patch)}
	b, _ := json.Marshal(result)
	return b
}

// pickImportedBool returns the first boolean value from the given keys, and
// nil when the source recorded no such flag: a default invented here would
// claim a decision the call did not make.
func pickImportedBool(args map[string]any, keys ...string) *bool {
	for _, k := range keys {
		if v, ok := args[k].(bool); ok {
			return &v
		}
	}
	return nil
}

// importedPickRequiredString returns the first of the given keys whose value
// is a string, reporting presence rather than non-emptiness: a present but
// empty value is the value the source wrote and carries over as it was, while
// a missing one leaves the call unreadable rather than inventing a value.
func importedPickRequiredString(args map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := args[k].(string); ok {
			return v, true
		}
	}
	return "", false
}
