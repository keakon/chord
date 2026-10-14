package agent

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/agent/toolefficiency"
	"github.com/keakon/chord/internal/toolname"
)

// efficiencyNoteForToolResult adapts unaudited tool output and wrapped JSON
// arguments into read evidence. The state consumes advice before the result's
// append hook runs, preserving the model-facing tool-result tail.
func (t *Turn) efficiencyNoteForToolResult(callID, toolName, argsJSON, rawResult string, failed bool) string {
	if t == nil {
		return ""
	}
	var read toolefficiency.ReadObservation
	if !failed && toolname.Normalize(toolName) == toolname.Read {
		header, _, _ := strings.Cut(rawResult, "\n")
		if r := requesttrim.ParseReadResultHeaderRange(header); r.OK {
			read = toolefficiency.ReadObservation{
				Start: r.Start, End: r.End, Total: r.Total,
				Truncated: strings.Contains(header, "truncated="),
			}
			if read.ShouldTrack() {
				read.Path = toolPathFromArgs(json.RawMessage(argsJSON))
			}
		}
	}
	return t.Efficiency.NoteResult(callID, failed, read)
}
