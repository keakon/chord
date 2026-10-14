package requesttrim

import (
	"crypto/sha256"
	"io"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// Scan lazily computes and caches the whole-history scans
// (tool-call metadata, repeated-output detection, read-validity analysis)
// shared by the surface-review checks and the main reduction pass of a single
// prepare call. Several review call sites can run per request; sharing one
// scan avoids re-walking the full history at each of them.
//
// The scans run over the original messages. CallMeta and RepeatedOutputs
// depend only on roles, tool-call IDs, and assistant tool calls, which are
// identical in the prepared copy, so those results are also valid for it.
// ReadValidity additionally parses tool-result content, so it is only valid
// for the original messages; the main pass computes its own validity over the
// prepared copy. Not safe for concurrent use.
type Scan struct {
	messages      []message.Message
	memo          *Memo
	meta          map[string]ToolCall
	repeated      map[int]bool
	evidence      fileEvidenceView
	evidenceDone  bool
	evidenceStats EvidenceStats
}

// NewScan borrows an immutable history for one request. memo may be nil; a supplied
// memo is shared across requests and must be reset when its owning session changes.
func NewScan(messages []message.Message, memo *Memo) *Scan {
	return &Scan{messages: messages, memo: memo}
}

// CallMeta returns this scan's metadata map. Callers may enrich its per-call parse
// caches, but must not change call identity or arguments. It must not escape the request.
func (s *Scan) CallMeta() map[string]ToolCall {
	if s.meta == nil {
		s.meta = s.memo.cachedToolCallMeta(s.messages)
	}
	return s.meta
}

// RepeatedOutputs returns a borrowed map of superseded identical outputs.
// Callers must not mutate it or retain it beyond this request.
func (s *Scan) RepeatedOutputs() map[int]bool {
	if s.repeated == nil {
		s.repeated = detectRepeatedToolOutputs(s.messages, s.CallMeta(), s.memo)
	}
	return s.repeated
}

// ReadValidity returns independently owned validity results for this history.
func (s *Scan) ReadValidity() map[int]ReadValidity {
	return s.fileEvidence().ValidityByMessage()
}

// ContentHash hashes via io.WriteString so the whole content is
// not copied into a []byte before digesting; callers hash multi-KB tool
// outputs on every request.
func ContentHash(value string) [sha256.Size]byte {
	h := sha256.New()
	_, _ = io.WriteString(h, value)
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func contextReductionToolInputKey(toolName, args string) string {
	if strings.TrimSpace(toolName) == "" && strings.TrimSpace(args) == "" {
		return ""
	}
	return toolname.Normalize(toolName) + "\x00" + strings.TrimSpace(args)
}

// BuildToolCallMeta returns newly owned call metadata without changing the history.
func BuildToolCallMeta(messages []message.Message) map[string]ToolCall {
	meta := make(map[string]ToolCall)
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			meta[tc.ID] = ToolCall{Name: tools.NormalizeName(tc.Name), Args: string(tc.Args)}
		}
	}
	return meta
}

// RequestBatchesAfter reports tool-result age in main-model request batches.
// Persisted request IDs preserve failed-request gaps; legacy histories without
// IDs fall back to counting later assistant responses. All parallel tool calls
// declared by one assistant message share its request batch.
func RequestBatchesAfter(messages []message.Message, currentBatch uint64) []int {
	ages := make([]int, len(messages))
	batchByToolCall := make(map[string]uint64)
	for _, msg := range messages {
		if msg.Role != message.RoleAssistant || msg.RequestBatch == 0 {
			continue
		}
		for _, call := range msg.ToolCalls {
			batchByToolCall[call.ID] = msg.RequestBatch
		}
	}
	seenAssistants := 0
	seenUsers := 0
	for i := len(messages) - 1; i >= 0; i-- {
		ages[i] = max(seenAssistants, seenUsers)
		batch := messages[i].RequestBatch
		if messages[i].Role == message.RoleTool {
			batch = batchByToolCall[messages[i].ToolCallID]
		}
		if batch > 0 && currentBatch >= batch {
			ages[i] = int(currentBatch - batch)
		}
		if messages[i].Role == message.RoleAssistant {
			seenAssistants++
		}
		if messages[i].Role == message.RoleUser {
			seenUsers++
		}
	}
	return ages
}

// detectRepeatedToolOutputs marks tool results whose content survives verbatim
// in a later call with identical arguments. Identical arguments alone are not
// enough: a command can be deterministic (grep, cat) or time-varying (git
// status, go test, ls), and for the latter the older output is the "before"
// state the model may still be comparing against. Collapsing it to a marker
// that asserts the calls are identical would delete evidence and misstate what
// remains, so a differing older copy is left to the normal shape-based rules,
// which summarize it without claiming a fresher copy carries it.
func detectRepeatedToolOutputs(messages []message.Message, meta map[string]ToolCall, memo *Memo) map[int]bool {
	repeated := make(map[int]bool)
	seen := make(map[string][sha256.Size]byte)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != message.RoleTool {
			continue
		}
		call, ok := meta[msg.ToolCallID]
		if !ok {
			continue
		}
		key := memo.ToolInputKey(msg.ToolCallID, call)
		if digest, established := seen[key]; established {
			if digest == memo.toolResultDigest(msg.ToolCallID, msg.Content) {
				repeated[i] = true
			}
			// The newest trustworthy copy stays the comparison base: it is the
			// one the marker points at, and re-basing on an intermediate copy
			// would make the assertion false for everything before it.
			continue
		}
		// Only a trustworthy result establishes "a fresher identical output
		// exists later": explicit failures/cancellations must not make the
		// repeated marker point at an unsuccessful run. Content sniffing is
		// reserved for status-less legacy transcripts (matching the rendered
		// "Error:" prefix, as in message.ClassifyToolResultContent): an
		// explicit success that merely mentions "Error:" mid-output — a grep
		// over error handling, a log dump — is still a trustworthy copy.
		trustworthy := memo.toolResultTrustworthy(msg.ToolCallID, msg.ToolStatus, msg.Content)
		if trustworthy {
			seen[key] = memo.toolResultDigest(msg.ToolCallID, msg.Content)
		}
	}
	return repeated
}

// Messages returns the borrowed history. The caller must not mutate it while the scan is in use.
func (s *Scan) Messages() []message.Message { return s.messages }
