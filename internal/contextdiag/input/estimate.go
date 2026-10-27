package input

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

const (
	diagnosticMaxMessages    = contextdiag.MaxMessages
	diagnosticMaxFragments   = contextdiag.MaxFragments
	diagnosticMaxSources     = 256
	diagnosticMaxHashBytes   = 8 << 20
	diagnosticSchemaNodes    = 4096
	diagnosticSchemaBytes    = 256 << 10
	diagnosticBudget         = 20 * time.Millisecond
	diagnosticScanIncomplete = "scan_incomplete"
	diagnosticOpaque         = "opaque_native"
	diagnosticOverhead       = "adapter_protocol_overhead_unestimated"
	diagnosticOverrides      = "request_overrides"
	diagnosticProjection     = "projection_incomplete"
)

type diagnosticBudgetState struct {
	ctx                  context.Context
	deadline             time.Time
	fragments, hashBytes int
	incomplete           bool
	hashSize             [8]byte
	hashScratch          []byte
	schemas              map[*message.ToolDefinition]diagnosticSchema
	schemaBytes          int
}

func newDiagnosticBudget(ctx context.Context) *diagnosticBudgetState {
	return &diagnosticBudgetState{ctx: ctx, deadline: time.Now().Add(diagnosticBudget)}
}
func (b *diagnosticBudgetState) step() bool {
	b.fragments++
	if b.fragments > diagnosticMaxFragments || b.fragments%64 == 0 && (b.ctx.Err() != nil || time.Now().After(b.deadline)) {
		b.incomplete = true
	}
	return !b.incomplete
}
func saturatedAdd(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}
func addEstimate(a *contextdiag.Estimate, b contextdiag.Estimate) {
	a.Tokens = saturatedAdd(a.Tokens, b.Tokens)
	a.TextBytes = saturatedAdd(a.TextBytes, b.TextBytes)
	a.UnknownItems += b.UnknownItems
}
func diagnosticText(n int) contextdiag.Estimate {
	return contextdiag.Estimate{Tokens: int64(n)/3 + int64((n%3+2)/3), TextBytes: int64(n)}
}

// Check schemas before encoding: bounded nodes, depth and string bytes also
// reject cycles and custom marshalers without invoking arbitrary user code.
func boundedDiagnosticJSON(v any) ([]byte, bool) {
	nodes, bytes := 0, 0
	var visit func(reflect.Value, int) bool
	visit = func(v reflect.Value, depth int) bool {
		nodes++
		if nodes > diagnosticSchemaNodes || depth > 64 {
			return false
		}
		if !v.IsValid() {
			return true
		}
		if v.Type().NumMethod() > 0 {
			return false
		}
		if v.Kind() == reflect.Interface {
			return visit(v.Elem(), depth+1)
		}
		switch v.Kind() {
		case reflect.String:
			bytes += v.Len()
			return bytes <= diagnosticSchemaBytes
		case reflect.Map:
			if v.Type().Key().Kind() != reflect.String || v.Len() > diagnosticSchemaNodes-nodes {
				return false
			}
			it := v.MapRange()
			for it.Next() {
				if !visit(it.Key(), depth+1) || !visit(it.Value(), depth+1) {
					return false
				}
			}
			return true
		case reflect.Slice, reflect.Array:
			if v.Len() > diagnosticSchemaNodes-nodes {
				return false
			}
			for i := 0; i < v.Len(); i++ {
				if !visit(v.Index(i), depth+1) {
					return false
				}
			}
			return true
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			return true
		default:
			return false
		}
	}
	if !visit(reflect.ValueOf(v), 0) {
		return nil, false
	}
	data, err := json.Marshal(v)
	return data, err == nil
}

type diagnosticEstimateBuilder struct {
	categories  []contextdiag.Category
	sources     []map[string]*contextdiag.Source
	identities  int
	budget      *diagnosticBudgetState
	limitations []string
}

func (e *diagnosticEstimateBuilder) limitation(s string) {
	if !slices.Contains(e.limitations, s) {
		e.limitations = append(e.limitations, s)
	}
}
func (e *diagnosticEstimateBuilder) add(category, kind, label string, amount contextdiag.Estimate) {
	i := slices.IndexFunc(e.categories, func(c contextdiag.Category) bool { return c.Kind == category })
	if i < 0 {
		return
	}
	c := &e.categories[i]
	addEstimate(&c.Estimate, amount)
	if len(label) > diagnosticSchemaBytes {
		addEstimate(&c.Other, amount)
		e.limitation("sources_incomplete")
		return
	}
	key := kind + "\x00" + label
	source := e.sources[i][key]
	if source == nil {
		if e.identities >= diagnosticMaxSources {
			addEstimate(&c.Other, amount)
			e.limitation("sources_incomplete")
			return
		}
		source = &contextdiag.Source{Kind: kind, Label: contextdiag.DisplayLabel(label)}
		e.sources[i][key] = source
		e.identities++
	}
	source.Count++
	addEstimate(&source.Estimate, amount)
}
func (e *diagnosticEstimateBuilder) text(category, kind, label, text string) {
	if text != "" && e.budget.step() {
		e.add(category, kind, label, diagnosticText(len(text)))
	}
}
func (e *diagnosticEstimateBuilder) unknown(category, kind, label string) {
	e.add(category, kind, label, contextdiag.Estimate{UnknownItems: 1})
}
func (e *diagnosticEstimateBuilder) tool(d *message.ToolDefinition) {
	if !e.budget.step() {
		return
	}
	e.text(contextdiag.Tools, contextdiag.Tool, d.Name, d.Name)
	e.text(contextdiag.Tools, contextdiag.Tool, d.Name, d.Description)
	if data, ok := e.budget.toolSchema(d); ok {
		e.add(contextdiag.Tools, contextdiag.Tool, d.Name, diagnosticText(len(data)))
	} else {
		e.unknown(contextdiag.Tools, contextdiag.Tool, d.Name)
		e.limitation("schema_unavailable")
	}
}
func estimateDiagnosticInput(b *diagnosticBudgetState, system string, msgs []message.Message, defs []message.ToolDefinition, options Options) ([]contextdiag.Category, []string, string) {
	e := diagnosticEstimateBuilder{budget: b, limitations: []string{diagnosticOverhead}}
	for _, kind := range []string{contextdiag.System, contextdiag.Tools, contextdiag.Instructions, contextdiag.Conversation, contextdiag.ToolResults, contextdiag.Runtime, contextdiag.Attachments} {
		e.categories = append(e.categories, contextdiag.Category{Kind: kind})
		e.sources = append(e.sources, make(map[string]*contextdiag.Source))
	}
	e.text(contextdiag.System, contextdiag.Instructions, "system", system)
	for i := range defs {
		if b.incomplete {
			break
		}
		e.tool(&defs[i])
	}
	if options.NativeDefinitions != nil {
		for _, definition := range options.NativeDefinitions() {
			if !b.step() {
				break
			}
			if data, ok := boundedDiagnosticJSON(definition); ok {
				e.add(contextdiag.Tools, contextdiag.Tool, "native", diagnosticText(len(data)))
			} else {
				e.unknown(contextdiag.Tools, contextdiag.Tool, "native")
				e.limitation("schema_unavailable")
			}
		}
	}
	if options.HostedTool {
		e.unknown(contextdiag.Tools, contextdiag.Tool, "native")
		e.limitation("native_definition_unknown")
	}
	calls := make(map[string]message.ToolCall)
	for _, m := range msgs[:min(len(msgs), diagnosticMaxMessages)] {
		if !b.step() {
			break
		}
		for _, call := range m.ToolCalls {
			if !b.step() {
				break
			}
			if len(calls) < diagnosticMaxFragments {
				calls[call.ID] = call
			}
		}
	}
	for _, m := range msgs[:min(len(msgs), diagnosticMaxMessages)] {
		if !b.step() {
			break
		}
		for i := range m.MCPTools {
			if b.incomplete {
				break
			}
			e.tool(&m.MCPTools[i])
		}
		if m.NativeTools != nil && len(m.NativeTools.Items) > 0 || len(m.ResponsesOutput) > 0 || len(m.GeminiParts) > 0 {
			e.unknown(contextdiag.Conversation, contextdiag.Conversation, diagnosticOpaque)
			e.limitation(diagnosticOpaque)
			continue
		}
		category, kind, label := contextdiag.Conversation, contextdiag.Conversation, "conversation"
		switch m.Role {
		case message.RoleSystem:
			category, kind, label = contextdiag.Instructions, contextdiag.Instructions, "instructions"
		case message.RoleTool:
			category, kind, label = contextdiag.ToolResults, contextdiag.Tool, contextdiag.Unknown
			if call, ok := calls[m.ToolCallID]; ok {
				label = call.Name
				if call.Name == toolname.Skill && m.ToolStatus == message.ToolStatusSuccess && len(call.Args) <= diagnosticSchemaBytes {
					var args struct {
						Name string `json:"name"`
					}
					if json.Unmarshal([]byte(call.Args), &args) == nil && args.Name != "" {
						kind, label = contextdiag.Skill, args.Name
					}
				}
			}
		}
		switch m.Kind {
		case message.KindTurnOverlay, message.KindContextNotice, message.KindLSPFollowup, message.KindReplayEvidence, message.KindReplayContinuation, message.KindStreamContinue, message.KindLoopNotice, message.KindHookFeedback, message.KindSubAgentMailbox, message.KindQuestionResult, message.KindSkillDiscovery:
			category, kind, label = contextdiag.Runtime, contextdiag.Runtime, "runtime"
		case message.KindDirectoryInstructionProjection:
			category, kind, label = contextdiag.Instructions, contextdiag.Instructions, "instructions"
		case message.KindBackgroundResult:
			category, kind, label = contextdiag.ToolResults, contextdiag.Tool, "background"
		case message.KindQuestionState:
			e.limitation("local_state_in_projection")
			continue
		case "", message.KindThinkingReplayPrefix:
		default:
			e.limitation("source_unspecified")
		}
		if m.DiagnosticSource == message.DiagnosticSourceInstructions {
			category, kind, label = contextdiag.Instructions, contextdiag.Instructions, "environment"
		} else if m.DiagnosticSource == message.DiagnosticSourceMixed {
			label = "unspecified"
			e.limitation("source_unspecified")
		}
		if len(m.Parts) > 0 {
			for _, p := range m.Parts {
				if !b.step() {
					break
				}
				if p.Type == message.ContentPartText {
					e.text(category, kind, label, p.Text)
				} else {
					e.unknown(contextdiag.Attachments, contextdiag.Conversation, string(p.Type))
				}
			}
		} else {
			e.text(category, kind, label, m.Content)
		}
		e.text(category, kind, label, m.ReasoningContent)
		for _, thinking := range m.ThinkingBlocks {
			if !b.step() {
				break
			}
			e.text(category, kind, label, thinking.Thinking)
			if thinking.Data != "" || thinking.Signature != "" {
				e.unknown(contextdiag.Conversation, contextdiag.Conversation, "reasoning")
			}
		}
		for _, call := range m.ToolCalls {
			if !b.step() {
				break
			}
			e.text(contextdiag.Conversation, contextdiag.Conversation, "tool_calls", call.Name)
			if len(call.Args) > 0 && b.step() {
				e.add(contextdiag.Conversation, contextdiag.Conversation, "tool_calls", diagnosticText(len(call.Args)))
			}
			if call.ThoughtSignature != "" {
				e.unknown(contextdiag.Conversation, contextdiag.Conversation, "reasoning")
			}
		}
	}
	completeness := contextdiag.Estimated
	if len(msgs) > diagnosticMaxMessages || b.incomplete {
		e.limitation(diagnosticScanIncomplete)
		completeness = contextdiag.Partial
	}
	for i := range e.categories {
		c := &e.categories[i]
		if c.Estimate.UnknownItems > 0 {
			completeness = contextdiag.Partial
		}
		for _, s := range e.sources[i] {
			c.Sources = append(c.Sources, *s)
		}
		slices.SortFunc(c.Sources, func(a, b contextdiag.Source) int {
			if a.Estimate.Tokens > b.Estimate.Tokens {
				return -1
			}
			if a.Estimate.Tokens < b.Estimate.Tokens {
				return 1
			}
			return strings.Compare(a.Kind+"/"+a.Label, b.Kind+"/"+b.Label)
		})
		if len(c.Sources) > 5 {
			for _, s := range c.Sources[5:] {
				addEstimate(&c.Other, s.Estimate)
			}
			c.Sources = c.Sources[:5:5]
		}
	}
	return e.categories, e.limitations, completeness
}
