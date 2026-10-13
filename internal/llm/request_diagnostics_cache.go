package llm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"strconv"
	"time"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
)

type diagnosticFingerprint struct {
	digest [32]byte
	tokens int64
}
type diagnosticBaseline struct {
	domain                        [32]byte
	system, tools                 [32]byte
	systemOK, toolsOK, messagesOK bool
	messages                      []diagnosticFingerprint
}

// Length framing preserves boundaries even when adjacent fields are empty.
func diagnosticHashField(h hash.Hash, b *diagnosticBudgetState, s string) bool {
	if !b.step() || len(s) > diagnosticMaxHashBytes-b.hashBytes-8 {
		b.incomplete = true
		return false
	}
	binary.LittleEndian.PutUint64(b.hashSize[:], uint64(len(s)))
	h.Write(b.hashSize[:])
	b.hashBytes += 8
	for len(s) > 0 {
		n := min(len(s), 64<<10)
		if len(b.hashScratch) < n {
			b.hashScratch = make([]byte, n)
		}
		copy(b.hashScratch[:n], s[:n])
		h.Write(b.hashScratch[:n])
		s = s[n:]
		b.hashBytes += n
		if b.ctx.Err() != nil || time.Now().After(b.deadline) {
			b.incomplete = true
			return false
		}
	}
	return true
}
func diagnosticSum(h hash.Hash) (v [32]byte) { copy(v[:], h.Sum(nil)); return }
func diagnosticToolsHash(h hash.Hash, b *diagnosticBudgetState, defs []message.ToolDefinition) bool {
	if !diagnosticHashField(h, b, strconv.Itoa(len(defs))) {
		return false
	}
	for i := range defs {
		d := &defs[i]
		if !diagnosticHashField(h, b, d.Name) || !diagnosticHashField(h, b, d.Description) {
			return false
		}
		schema, ok := b.toolSchema(d)
		if !ok || !diagnosticHashField(h, b, string(schema)) {
			return false
		}
	}
	return true
}
func fingerprintDiagnosticMessage(b *diagnosticBudgetState, m message.Message) (diagnosticFingerprint, bool) {
	if m.NativeTools != nil && len(m.NativeTools.Items) > 0 || len(m.ResponsesOutput) > 0 || len(m.GeminiParts) > 0 {
		return diagnosticFingerprint{}, false
	}
	h := sha256.New()
	tokens := int64(0)
	field := func(s string) bool { return diagnosticHashField(h, b, s) }
	text := func(s string) bool { tokens = saturatedAdd(tokens, diagnosticText(len(s)).Tokens); return field(s) }
	if !field(string(m.Role)) || !field(m.ToolCallID) || !field(strconv.Itoa(len(m.Parts))) {
		return diagnosticFingerprint{}, false
	}
	if len(m.Parts) == 0 {
		if !text(m.Content) {
			return diagnosticFingerprint{}, false
		}
	} else {
		for _, p := range m.Parts {
			if p.Type != message.ContentPartText || !field(string(p.Type)) || !text(p.Text) {
				return diagnosticFingerprint{}, false
			}
		}
	}
	if !text(m.ReasoningContent) || !field(strconv.Itoa(len(m.ThinkingBlocks))) {
		return diagnosticFingerprint{}, false
	}
	for _, v := range m.ThinkingBlocks {
		if v.Data != "" || v.Signature != "" || !text(v.Thinking) {
			return diagnosticFingerprint{}, false
		}
	}
	if !field(strconv.Itoa(len(m.ToolCalls))) {
		return diagnosticFingerprint{}, false
	}
	for _, v := range m.ToolCalls {
		if len(v.Args) > diagnosticMaxHashBytes-b.hashBytes {
			b.incomplete = true
			return diagnosticFingerprint{}, false
		}
		if !field(v.ID) || !text(v.Name) || !text(string(v.Args)) || !field(v.ThoughtSignature) {
			return diagnosticFingerprint{}, false
		}
	}
	if !diagnosticToolsHash(h, b, m.MCPTools) {
		return diagnosticFingerprint{}, false
	}
	return diagnosticFingerprint{diagnosticSum(h), tokens}, true
}
func buildDiagnosticBaseline(b *diagnosticBudgetState, system string, msgs []message.Message, defs []message.ToolDefinition) *diagnosticBaseline {
	out := &diagnosticBaseline{}
	h := sha256.New()
	out.systemOK = diagnosticHashField(h, b, system)
	out.system = diagnosticSum(h)
	h = sha256.New()
	out.toolsOK = diagnosticToolsHash(h, b, defs)
	out.tools = diagnosticSum(h)
	out.messagesOK = len(msgs) <= diagnosticMaxMessages
	if out.messagesOK {
		for _, m := range msgs {
			f, ok := fingerprintDiagnosticMessage(b, m)
			if !ok {
				out.messagesOK = false
				break
			}
			out.messages = append(out.messages, f)
		}
	}
	if !out.messagesOK {
		out.messages = nil
	}
	return out
}
func compareDiagnosticBaseline(previous, current *diagnosticBaseline) contextdiag.Cache {
	c := contextdiag.Cache{BaselineState: contextdiag.First, PrefixState: contextdiag.Unknown, Explanation: contextdiag.NoReport}
	if previous == nil {
		return c
	}
	if previous.domain != current.domain {
		c.BaselineState = contextdiag.DomainChanged
		return c
	}
	c.BaselineState = contextdiag.Comparable
	if previous.systemOK && current.systemOK && previous.system != current.system {
		c.LocalChanges = append(c.LocalChanges, contextdiag.System)
	}
	if previous.toolsOK && current.toolsOK && previous.tools != current.tools {
		c.LocalChanges = append(c.LocalChanges, contextdiag.Tools)
	}
	if !previous.messagesOK || !current.messagesOK {
		return c
	}
	i := 0
	for i < len(previous.messages) && i < len(current.messages) && previous.messages[i].digest == current.messages[i].digest {
		c.ExpectedPrefix = saturatedAdd(c.ExpectedPrefix, current.messages[i].tokens)
		i++
	}
	switch {
	case i == len(previous.messages) && i == len(current.messages):
		c.PrefixState = contextdiag.Same
	case i == len(previous.messages):
		c.PrefixState = contextdiag.Append
	case i == len(current.messages):
		c.PrefixState = contextdiag.Truncate
	default:
		c.PrefixState = contextdiag.Rewrite
	}
	if c.PrefixState == contextdiag.Truncate || c.PrefixState == contextdiag.Rewrite {
		c.LocalChanges = append(c.LocalChanges, contextdiag.Messages)
	}
	c.ExpectedUsable = previous.systemOK && current.systemOK && previous.toolsOK && current.toolsOK && previous.system == current.system && previous.tools == current.tools
	return c
}
func diagnosticDomain(secret []byte, fields ...string) [32]byte {
	h := hmac.New(sha256.New, secret)
	for _, s := range fields {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(s)))
		h.Write(size[:])
		h.Write([]byte(s))
	}
	return diagnosticSum(h)
}
