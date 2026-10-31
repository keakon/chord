package requesttrim

import (
	"crypto/sha256"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Memo caches the byte-derived per-call verdicts the request
// reduction pass recomputes on every LLM request: tool-call metadata (the
// args-string copy), the repeat-detection input key, the tool-result content
// digest, and the single-invocation shell parse. Each verdict is a pure
// function of message content that is immutable once persisted, so a value
// computed once per ToolCallID stays valid for the message's lifetime. The
// request pass still walks the history to rebuild its ID→verdict map and to run
// repeat detection; what the memo removes is the per-call verdict computation
// itself, which otherwise re-copied every call's args, re-hashed every result
// body, and re-parsed every shell invocation on every request.
//
// Each map is bounded: once full it keeps roughly half its entries and drops
// the rest, so a session that crosses the cap degrades to partial recompute
// instead of re-deriving every verdict on every request. Reduction-cache
// clears (session restore, compaction apply, model-identity change) reset the
// memo outright.
//
// All methods are nil-receiver safe: a nil memo computes directly, which is
// what agent-less scans (unit tests) fall back to.
type Memo struct {
	mu          sync.Mutex
	meta        map[string]ToolCall
	inputKeys   map[string]string
	digests     map[string][sha256.Size]byte
	trustworthy map[string]bool
	shell       map[string]shellInvocationParse
}

// reductionToolCallMemoMaxEntries bounds each memo map so a pathological
// session cannot grow it without limit; every cached verdict is recomputable.
const reductionToolCallMemoMaxEntries = 8192

// memoMapForInsert returns the map to insert into: a nil map gets a fresh
// small allocation, a full map keeps roughly half its entries (arbitrary
// which, since Go map iteration order is unspecified — any half preserves the
// bulk of the hit rate) and reuses its backing array.
func memoMapForInsert[V any](m map[string]V, maxEntries int) map[string]V {
	if m == nil {
		return make(map[string]V, 64)
	}
	if len(m) < maxEntries {
		return m
	}
	for k := range m {
		if len(m) <= maxEntries/2 {
			return m
		}
		delete(m, k)
	}
	return m
}

// shellInvocationParse is the memoized singleShellInvocationLiteralArgs result
// for one (tool call, program) pair.
type shellInvocationParse struct {
	literal []string
	ok      bool
}

func (m *Memo) toolCallMetaFor(toolCallID, name string, args []byte) ToolCall {
	if m == nil || toolCallID == "" {
		return ToolCall{Name: tools.NormalizeName(name), Args: string(args)}
	}
	m.mu.Lock()
	if v, ok := m.meta[toolCallID]; ok {
		m.mu.Unlock()
		return v
	}
	m.meta = memoMapForInsert(m.meta, reductionToolCallMemoMaxEntries)
	v := ToolCall{Name: tools.NormalizeName(name), Args: string(args)}
	m.meta[toolCallID] = v
	m.mu.Unlock()
	return v
}

// cachedToolCallMeta builds the ToolCallID → metadata map over the whole
// history, reusing the memoized args string for every call seen before.
func (m *Memo) cachedToolCallMeta(messages []message.Message) map[string]ToolCall {
	if m == nil {
		return BuildToolCallMeta(messages)
	}
	var calls int
	for i := range messages {
		calls += len(messages[i].ToolCalls)
	}
	meta := make(map[string]ToolCall, calls)
	for i := range messages {
		for _, tc := range messages[i].ToolCalls {
			meta[tc.ID] = m.toolCallMetaFor(tc.ID, tc.Name, tc.Args)
		}
	}
	return meta
}

func (m *Memo) toolResultDigest(toolCallID, content string) [sha256.Size]byte {
	if m == nil || toolCallID == "" {
		return ContentHash(content)
	}
	m.mu.Lock()
	if digest, ok := m.digests[toolCallID]; ok {
		m.mu.Unlock()
		return digest
	}
	m.digests = memoMapForInsert(m.digests, reductionToolCallMemoMaxEntries)
	digest := ContentHash(content)
	m.digests[toolCallID] = digest
	m.mu.Unlock()
	return digest
}

// ToolInputKey memoizes normalized call identity for immutable tool-call arguments.
func (m *Memo) ToolInputKey(toolCallID string, meta ToolCall) string {
	if m == nil || toolCallID == "" {
		return contextReductionToolInputKey(meta.Name, meta.Args)
	}
	m.mu.Lock()
	if key, ok := m.inputKeys[toolCallID]; ok {
		m.mu.Unlock()
		return key
	}
	m.inputKeys = memoMapForInsert(m.inputKeys, reductionToolCallMemoMaxEntries)
	key := contextReductionToolInputKey(meta.Name, meta.Args)
	m.inputKeys[toolCallID] = key
	m.mu.Unlock()
	return key
}

// shellInvocationLiteralArgs is the memoized singleShellInvocationLiteralArgs:
// the JSON decode and the bash AST parse run once per (tool call, program)
// pair instead of once per shell result per request.
func (m *Memo) shellInvocationLiteralArgs(toolCallID, argsJSON, program string) ([]string, bool) {
	if m == nil || toolCallID == "" {
		return singleShellInvocationLiteralArgs(argsJSON, program)
	}
	key := toolCallID + "\x00" + program
	m.mu.Lock()
	if parse, ok := m.shell[key]; ok {
		m.mu.Unlock()
		return parse.literal, parse.ok
	}
	m.mu.Unlock()
	// The bash AST parse runs unlocked, so Reset() may have dropped the map in
	// the meantime; re-check capacity after re-acquiring rather than before.
	literal, ok := singleShellInvocationLiteralArgs(argsJSON, program)
	m.mu.Lock()
	m.shell = memoMapForInsert(m.shell, reductionToolCallMemoMaxEntries)
	m.shell[key] = shellInvocationParse{literal: literal, ok: ok}
	m.mu.Unlock()
	return literal, ok
}

// toolResultTrustworthy memoizes the repeat-detection trustworthiness verdict:
// whether this result may serve as the "fresher identical output exists later"
// comparison base. It is a pure function of the (immutable) status and
// content, and the content scan behind it runs per tool result per request.
func (m *Memo) toolResultTrustworthy(toolCallID, toolStatus, content string) bool {
	if m == nil || toolCallID == "" {
		return message.IsToolResultSuccessStatus(toolStatus) ||
			(strings.TrimSpace(toolStatus) == "" && !message.IsToolErrorContent(content))
	}
	m.mu.Lock()
	if cached, ok := m.trustworthy[toolCallID]; ok {
		m.mu.Unlock()
		return cached
	}
	m.trustworthy = memoMapForInsert(m.trustworthy, reductionToolCallMemoMaxEntries)
	verdict := message.IsToolResultSuccessStatus(toolStatus) ||
		(strings.TrimSpace(toolStatus) == "" && !message.IsToolErrorContent(content))
	m.trustworthy[toolCallID] = verdict
	m.mu.Unlock()
	return verdict
}

// Reset drops all cached verdicts. The owner calls it on session restoration,
// compaction application and request identity changes; an empty memo is ready for reuse.
func (m *Memo) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.meta = nil
	m.inputKeys = nil
	m.digests = nil
	m.trustworthy = nil
	m.shell = nil
	m.mu.Unlock()
}
