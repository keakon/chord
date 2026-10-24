package externalclaude

import (
	"encoding/json"

	"github.com/keakon/chord/internal/toolname"
)

// Mapping classifies a Claude tool call in Chord's tool vocabulary. The same
// table drives permission evaluation and TUI card selection.
type Mapping struct {
	ChordName string
}

// MapTool translates a Claude tool name and input into the Chord tool name and
// chord-shaped input. ok is false when Chord has no equivalent; such calls are
// shown as generic cards and are never auto-allowed by a chord ruleset match.
func MapTool(claudeName string, input json.RawMessage) (mapping Mapping, chordInput json.RawMessage, ok bool) {
	claudeName = NormalizeToolName(claudeName)
	switch claudeName {
	case "Read":
		return Mapping{ChordName: toolname.Read}, remapFields(input, map[string]string{"file_path": "path"}), true
	case "Edit":
		return Mapping{ChordName: toolname.Edit}, remapFields(input, map[string]string{"file_path": "path"}), true
	case "Write":
		return Mapping{ChordName: toolname.Write}, remapFields(input, map[string]string{"file_path": "path"}), true
	case "NotebookEdit":
		return Mapping{ChordName: toolname.NotebookEdit},
			remapFields(input, map[string]string{"notebook_path": "path"}), true
	case "AskUserQuestion":
		return Mapping{ChordName: toolname.Question}, input, true
	case "Bash":
		return Mapping{ChordName: toolname.Shell}, remapFields(input, map[string]string{"timeout": "timeout_ms"}), true
	case "Glob":
		return Mapping{ChordName: toolname.Glob}, remapGlob(input), true
	case "Grep":
		return Mapping{ChordName: toolname.Grep}, remapGrep(input), true
	case "WebFetch":
		return Mapping{ChordName: toolname.WebFetch}, remapFields(input, nil), true
	case "WebSearch":
		return Mapping{ChordName: toolname.WebSearch}, remapFields(input, nil), true
	case "TodoWrite":
		return Mapping{ChordName: toolname.TodoWrite}, remapFields(input, nil), true
	case MCPNotifyToolName:
		return Mapping{ChordName: toolname.Notify}, input, true
	default:
		return Mapping{}, input, false
	}
}

func remapFields(raw json.RawMessage, rename map[string]string) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}
	for from, to := range rename {
		value, ok := fields[from]
		if !ok {
			continue
		}
		delete(fields, from)
		fields[to] = value
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

func remapGlob(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}
	if pattern, ok := fields["pattern"]; ok {
		delete(fields, "pattern")
		fields["patterns"] = wrapArray(pattern)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

func remapGrep(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}
	if path, ok := fields["path"]; ok {
		delete(fields, "path")
		fields["paths"] = wrapArray(path)
	}
	if glob, ok := fields["glob"]; ok {
		delete(fields, "glob")
		fields["includes"] = wrapArray(glob)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

func wrapArray(value json.RawMessage) json.RawMessage {
	array, err := json.Marshal([]json.RawMessage{value})
	if err != nil {
		return value
	}
	return array
}
