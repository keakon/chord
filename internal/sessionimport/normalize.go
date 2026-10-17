package sessionimport

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// normalizeImportedMessages repairs converted history that no provider could
// replay as-is. A tool result with no text — a codex `exec` script that printed
// nothing, a source block that carried only an envelope — is replaced by the
// text a live Chord call leaves behind for the same outcome, keyed by the
// paired call's tool name and status: the model reads "ran, no output" instead
// of a request the provider rejects with "missing required parameter", and the
// user sees exactly what the model sees. It returns how many results it filled
// in.
func normalizeImportedMessages(msgs []message.Message) int {
	callNames := make(map[string]string, len(msgs))
	for i := range msgs {
		if msgs[i].Role != message.RoleAssistant {
			continue
		}
		for _, tc := range msgs[i].ToolCalls {
			if id := strings.TrimSpace(tc.ID); id != "" {
				callNames[id] = tc.Name
			}
		}
	}
	filled := 0
	for i := range msgs {
		if msgs[i].Role != message.RoleTool || msgs[i].Content != "" || len(msgs[i].Parts) > 0 {
			continue
		}
		msgs[i].Content = importedEmptyResultText(callNames[msgs[i].ToolCallID], msgs[i].ToolStatus)
		filled++
	}
	return filled
}

// importedEmptyResultText picks the stand-in for an empty tool result by the
// outcome the source recorded, always a string the live path produces for the
// same outcome:
//
//   - a finished success keeps NormalizeEmptySuccessOutput's marker, so an
//     imported shell result reads "(shell completed with no output)" exactly
//     like a live one;
//   - a failure keeps the generic error line composeToolResultTexts falls back
//     to when an error carries no text of its own;
//   - a cancellation keeps the line a cancelled live call produces.
//
// A single "completed" marker for every status would claim a failed or
// cancelled call succeeded, and an import-flavored marker would tell the user
// something the model never sees.
func importedEmptyResultText(toolName string, status string) string {
	switch status {
	case message.ToolStatusError:
		return "Error: tool execution failed"
	case message.ToolStatusCancelled:
		return "Cancelled"
	default:
		return tools.NormalizeEmptySuccessOutput(toolName, "", nil)
	}
}
