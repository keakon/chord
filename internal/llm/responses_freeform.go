package llm

import (
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

// responsesApplyPatchLarkGrammar is the Codex apply_patch grammar attached to
// custom tool definitions. The client-side ParseApplyPatch implementation
// remains more tolerant so legacy or unconstrained responses still have a
// fallback parser.
const responsesApplyPatchLarkGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change_move? change?

filename: /(.+)/
add_line: "+" /(.*)/ LF -> line

change_move: "*** Move to: " filename LF
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF

%import common.LF
`

// responsesToolFormat is the constraint-decoding format block of a custom tool
// definition (Responses API custom tools).
type responsesToolFormat struct {
	Type       string `json:"type"`       // "grammar"
	Syntax     string `json:"syntax"`     // "lark"
	Definition string `json:"definition"` // Lark grammar text
}

// convertToolsToResponsesForTarget converts tool definitions to Responses API
// format for a specific provider/model target. apply_patch is emitted as a
// freeform custom tool (type:"custom" + grammar, no parameters) when the
// target supports it; every other tool stays a JSON function tool. All request
// paths that declare tools — the ordinary HTTP request, the Codex WebSocket
// request (which reuses the same responsesRequest tools), and remote
// compaction — share this entry so the freeform decision is identical
// everywhere.
func convertToolsToResponsesForTarget(provider *ProviderConfig, modelID string, tools []message.ToolDefinition) []responsesTool {
	result := make([]responsesTool, 0, len(tools))
	if len(tools) == 0 {
		return result
	}
	freeform := shouldEmitFreeformApplyPatch(provider, modelID)
	for _, t := range tools {
		if freeform && t.Name == toolname.ApplyPatch {
			// The custom tool wire description matches Codex exactly (the
			// FREEFORM sentence is only valid for this shape, so it cannot
			// live on the shared ToolDefinition description used by the JSON
			// function shape). gpt-5-and-later models are already trained on
			// the patch format and need no format teaching here.
			result = append(result, responsesTool{
				Type:        "custom",
				Name:        t.Name,
				Description: "The `apply_patch` tool can be used to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
				Format: &responsesToolFormat{
					Type:       "grammar",
					Syntax:     "lark",
					Definition: responsesApplyPatchLarkGrammar,
				},
			})
			continue
		}
		result = append(result, responsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return result
}

// shouldEmitFreeformApplyPatch reports whether apply_patch should be emitted as
// a freeform custom tool (type:"custom" + grammar) rather than a JSON function
// tool. Decision priority: an explicit compat.apply_patch.freeform override,
// then the endpoint constraint (non-Responses wires have no custom tool type),
// then the model-name whitelist. The two dimensions stay independent: an
// edit-default model never emits freeform, and a gpt-5 family model on a
// non-Responses endpoint emits the function shape. Hosts that accept Responses
// but reject custom tools have no built-in exception: set
// compat.apply_patch.freeform: false there (see docs/edit-tools.md).
func shouldEmitFreeformApplyPatch(provider *ProviderConfig, modelID string) bool {
	if provider != nil {
		if ap := provider.ApplyPatchCompat(modelID); ap != nil && ap.Freeform != nil {
			return *ap.Freeform
		}
		// Non-Responses adapters have no custom tool type; the model-name
		// inference below only runs for Responses providers.
		if provider.Type() != config.ProviderTypeResponses {
			return false
		}
	}
	return IsApplyPatchModel(modelID)
}

// IsApplyPatchModel reports whether the model defaults to the complete
// apply_patch tool-surface semantics (apply_patch instead of edit, with
// write/delete hidden). Every gpt major family from gpt-5 onward — bare
// gpt-5, gpt-5-mini, gpt-5-codex, every dotted gpt-5.x name, and future
// families such as gpt-6 — carries the patch training signal: apply_patch is
// the first-party Codex editing tool and stays in OpenAI training data across
// generations, mirroring the Codex model catalog whose gpt-5 entries all mark
// apply_patch_tool_type: freeform. codex-auto-review is the explicit review
// alias. Other *-codex names (daybreak-codex, foo-codex-bar, ...) are not gpt-5
// and default to edit. This name-based inference only decides the model
// dimension: whether the wire actually carries the freeform custom-tool shape
// (type:"custom" + grammar) additionally depends on the endpoint, which
// shouldEmitFreeformApplyPatch resolves. compat.apply_patch.enabled overrides
// the tool surface without changing the wire shape, and vice versa.
func IsApplyPatchModel(modelID string) bool {
	modelID = strings.ToLower(modelID)
	return isGPTApplyPatchModel(modelID) || modelID == "codex-auto-review"
}

// isGPTApplyPatchModel matches gpt families by major version: gpt-5 and every
// later major (gpt-6, gpt-7, ...) are patch-native, in bare (gpt-5), undotted
// subfamily (gpt-5-mini), and dotted subfamily (gpt-5.5) forms. Lookalike
// names (gpt-5x, gpt-v5) and unrelated products (gpt-oss-*) do not match.
func isGPTApplyPatchModel(modelID string) bool {
	rest, ok := strings.CutPrefix(modelID, "gpt-")
	if !ok {
		return false
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	major, err := strconv.Atoi(rest[:i])
	if err != nil || major < 5 {
		return false
	}
	// The numeric major must be followed by a family separator ('.' or '-')
	// or the end of the name, so lookalikes like gpt-5x stay unmatched.
	return i == len(rest) || rest[i] == '.' || rest[i] == '-'
}
