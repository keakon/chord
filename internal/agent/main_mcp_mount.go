package agent

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/message"
	toolpkg "github.com/keakon/chord/internal/tools"
)

type mcpToolMountMode uint8

const (
	mcpMountFullInjection mcpToolMountMode = iota
	mcpMountKimiDynamic
	mcpMountResponsesAdditionalTools
)

func (m mcpToolMountMode) cacheFriendly() bool {
	return m == mcpMountKimiDynamic || m == mcpMountResponsesAdditionalTools
}

func (a *MainAgent) mcpToolMountMode() mcpToolMountMode {
	if a.mcpMountFullInjectionOnly.Load() {
		return mcpMountFullInjection
	}
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		return mcpMountFullInjection
	}
	if client.AllPoolTargetsSupportKimiDynamicTools() {
		return mcpMountKimiDynamic
	}
	if client.AllPoolTargetsSupportResponsesAdditionalTools() {
		return mcpMountResponsesAdditionalTools
	}
	return mcpMountFullInjection
}

type manualMCPTool interface {
	IsManual() bool
}

func splitVisibleLLMTools(all []toolpkg.Tool) (stable, manual []toolpkg.Tool) {
	stable = make([]toolpkg.Tool, 0, len(all))
	for _, tool := range all {
		if mcpTool, ok := tool.(manualMCPTool); ok && mcpTool.IsManual() {
			manual = append(manual, tool)
			continue
		}
		stable = append(stable, tool)
	}
	return stable, manual
}

func (a *MainAgent) stableVisibleLLMTools() []toolpkg.Tool {
	all := a.mainVisibleLLMTools()
	mode := a.mcpToolMountMode()
	if !mode.cacheFriendly() || a.mcpToolMountFellBack(mode) {
		return all
	}
	stable, manual := splitVisibleLLMTools(all)
	a.mcpMountState.mu.Lock()
	baseline := a.mcpMountState.topLevelManualLocked(manual)
	a.mcpMountState.mu.Unlock()
	if len(baseline) == 0 {
		return stable
	}
	for _, tool := range manual {
		if _, ok := baseline[toolpkg.NormalizeName(tool.Name())]; ok {
			stable = append(stable, tool)
		}
	}
	return stable
}

// topLevelManualLocked returns the manual MCP tools carried in the top-level
// tools array for this session run, initializing the set on the first surface
// build. Before the first request there is no prompt cache to protect, so
// every manual tool enabled by then rides in the plain tools array; only tools
// enabled afterwards use the incremental declarations. The returned map is
// never mutated after initialization, so callers may read it outside the lock.
func (s *mcpToolMountState) topLevelManualLocked(manual []toolpkg.Tool) map[string]struct{} {
	if s.topLevelManual == nil {
		baseline := make(map[string]struct{}, len(manual))
		for _, tool := range manual {
			baseline[toolpkg.NormalizeName(tool.Name())] = struct{}{}
		}
		s.topLevelManual = baseline
	}
	return s.topLevelManual
}

// declaredRuntimeMCPToolDefsLocked returns the definitions of manual MCP tools
// that must be declared incrementally: everything visible except the top-level
// baseline. Callers must hold s.mu via the owning agent's mount state.
func (a *MainAgent) declaredRuntimeMCPToolDefsLocked() []message.ToolDefinition {
	_, manual := splitVisibleLLMTools(a.mainVisibleLLMTools())
	baseline := a.mcpMountState.topLevelManualLocked(manual)
	if len(baseline) > 0 {
		declared := manual[:0]
		for _, tool := range manual {
			if _, ok := baseline[toolpkg.NormalizeName(tool.Name())]; !ok {
				declared = append(declared, tool)
			}
		}
		manual = declared
	}
	return llmToolDefinitionsFromVisibleTools(manual)
}

type mcpMountAnchor struct {
	window     []stableReductionMessageShape
	occurrence int
}

type mcpMountSnapshot struct {
	anchor mcpMountAnchor
	tools  []message.ToolDefinition
}

type mcpToolMountState struct {
	mu        sync.Mutex
	mode      mcpToolMountMode
	snapshots []mcpMountSnapshot
	known     map[string][sha256.Size]byte
	fallback  bool
	// topLevelManual is the set of manual MCP tool names riding in the
	// top-level tools array for this session run (enabled before the first
	// surface build). nil until the first build; never mutated afterwards.
	topLevelManual map[string]struct{}
	messageShapes  []stableReductionMessageShape
	messageSources []message.Message
}

func (a *MainAgent) resetMCPToolMountState() {
	a.mcpMountState.mu.Lock()
	a.mcpMountState.mode = mcpMountFullInjection
	a.mcpMountState.snapshots = nil
	a.mcpMountState.known = nil
	a.mcpMountState.fallback = false
	a.mcpMountState.topLevelManual = nil
	a.mcpMountState.messageShapes = nil
	a.mcpMountState.messageSources = nil
	a.mcpMountState.mu.Unlock()
}

// forceFullMCPToolInjection reverts the session run to top-level MCP tool
// injection and disables cache-friendly dynamic mounts (Responses
// additional_tools and Kimi mcp_system_tools_message) until the next
// session-head event resets the surface. It is used at boundaries where
// prompt-cache reuse no longer applies — model switch, session resume, forked
// history, and durable compaction — because there the dynamic mounts would
// only risk being mis-anchored or misread by the model as the complete tool
// surface.
func (a *MainAgent) forceFullMCPToolInjection() {
	a.resetMCPMountSurface(true)
}

// resetMCPMountSurface clears the dynamic-mount bookkeeping and rebuilds the
// runtime tool/prompt surface. fullInjectionOnly pins the run to top-level
// injection; false re-enables cache-friendly mounts for a fresh session run.
func (a *MainAgent) resetMCPMountSurface(fullInjectionOnly bool) {
	a.mcpMountFullInjectionOnly.Store(fullInjectionOnly)
	a.resetMCPToolMountState()
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = a.mcpRuntimePrompt
	a.mcpServersPromptMu.Unlock()
	a.clearFrozenToolSurface()
	a.markRuntimeSurfaceDirty()
}

func (a *MainAgent) mcpToolMountFellBack(mode mcpToolMountMode) bool {
	a.mcpMountState.mu.Lock()
	defer a.mcpMountState.mu.Unlock()
	if a.mcpMountState.mode != mode {
		return false
	}
	return a.mcpMountState.fallback
}

// effectiveMCPMountIsFullInjection reports whether the current request surface
// actually injects every MCP tool at top level — either because the mount mode
// is full injection or because a cache-friendly mount has fallen back.
func (a *MainAgent) effectiveMCPMountIsFullInjection() bool {
	mode := a.mcpToolMountMode()
	return !mode.cacheFriendly() || a.mcpToolMountFellBack(mode)
}

// mountRuntimeMCPTools replays every previously emitted declaration at its
// original request position, then appends only newly discovered/changed tool
// schemas at the current conversation tail. If request reduction makes an old
// anchor unresolvable, it falls back to ordinary top-level injection rather
// than moving a declaration behind historical tool calls.
func (a *MainAgent) mountRuntimeMCPTools(messages []message.Message, mode mcpToolMountMode) ([]message.Message, []message.ToolDefinition, bool) {
	if !mode.cacheFriendly() {
		return messages, nil, false
	}
	state := &a.mcpMountState
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.mode != mode {
		state.mode = mode
		state.snapshots = nil
		state.known = nil
		state.fallback = false
	}
	active := a.declaredRuntimeMCPToolDefsLocked()
	if state.fallback {
		// Once fallback is sticky the rebuilt frozen surface already carries
		// every manual MCP tool (stableVisibleLLMTools returns the full list),
		// so returning defs here would duplicate them in the request.
		return messages, nil, false
	}

	activeNames := make(map[string]struct{}, len(active))
	for _, def := range active {
		activeNames[toolpkg.NormalizeName(def.Name)] = struct{}{}
	}
	if len(state.snapshots) == 0 && len(activeNames) > 0 && hasHistoricalMCPToolCall(messages, activeNames) {
		state.fallback = true
		return messages, active, true
	}
	shapes := state.incrementalMessageShapes(messages, a.promptGitStatus())

	positions := make([]int, 0, len(state.snapshots)+1)
	lastPosition := 0
	for _, snapshot := range state.snapshots {
		position, ok := findMCPMountAnchor(shapes, snapshot.anchor)
		if !ok || position < lastPosition {
			state.fallback = true
			return messages, active, true
		}
		positions = append(positions, position)
		lastPosition = position
	}

	if state.known == nil {
		state.known = make(map[string][sha256.Size]byte)
	}
	changed := make([]message.ToolDefinition, 0, len(active))
	for _, def := range active {
		name := toolpkg.NormalizeName(def.Name)
		sig := toolDefinitionSignature(def)
		if previous, ok := state.known[name]; ok && previous == sig {
			continue
		}
		state.known[name] = sig
		changed = append(changed, def)
	}
	if len(changed) > 0 {
		snapshot := mcpMountSnapshot{
			anchor: newMCPMountAnchor(shapes),
			tools:  append([]message.ToolDefinition(nil), changed...),
		}
		state.snapshots = append(state.snapshots, snapshot)
		positions = append(positions, len(messages))
	}
	if len(state.snapshots) == 0 {
		return messages, nil, false
	}

	out := make([]message.Message, 0, len(messages)+len(state.snapshots))
	snapshotIndex := 0
	for position := 0; position <= len(messages); position++ {
		for snapshotIndex < len(state.snapshots) && positions[snapshotIndex] == position {
			out = append(out, message.NewSystemToolsMessage(state.snapshots[snapshotIndex].tools))
			snapshotIndex++
		}
		if position < len(messages) {
			out = append(out, messages[position])
		}
	}
	return out, nil, false
}

const mcpMountAnchorWindow = 3

func newMCPMountAnchor(shapes []stableReductionMessageShape) mcpMountAnchor {
	windowLen := min(mcpMountAnchorWindow, len(shapes))
	window := make([]stableReductionMessageShape, windowLen)
	copy(window, shapes[len(shapes)-windowLen:])
	occurrence := 1
	for start := 0; start+windowLen < len(shapes); start++ {
		if mcpMountWindowMatches(shapes, start, window) {
			occurrence++
		}
	}
	return mcpMountAnchor{window: window, occurrence: occurrence}
}

func findMCPMountAnchor(shapes []stableReductionMessageShape, anchor mcpMountAnchor) (int, bool) {
	if len(anchor.window) == 0 {
		return 0, true
	}
	seen := 0
	for start := 0; start+len(anchor.window) <= len(shapes); start++ {
		if !mcpMountWindowMatches(shapes, start, anchor.window) {
			continue
		}
		seen++
		if seen == anchor.occurrence {
			return start + len(anchor.window), true
		}
	}
	return 0, false
}

func mcpMountWindowMatches(shapes []stableReductionMessageShape, start int, window []stableReductionMessageShape) bool {
	for i, want := range window {
		if shapes[start+i] != want {
			return false
		}
	}
	return true
}

func (a *MainAgent) promptGitStatus() string {
	_, gitStatus, _, _ := a.promptMetaSnapshot()
	return gitStatus
}

func (s *mcpToolMountState) incrementalMessageShapes(messages []message.Message, gitStatus string) []stableReductionMessageShape {
	messages = normalizeMCPMountMessages(messages, gitStatus)
	reusable := 0
	if len(s.messageSources) == len(s.messageShapes) {
		limit := min(len(s.messageSources), len(messages))
		for reusable < limit && stableReductionMessageEquivalent(&s.messageSources[reusable], &messages[reusable]) {
			reusable++
		}
		if reusable == len(messages) && len(s.messageSources) == len(messages) {
			return s.messageShapes
		}
	}
	s.messageSources = append(s.messageSources[:reusable], messages[reusable:]...)
	s.messageShapes = append(s.messageShapes[:reusable], make([]stableReductionMessageShape, len(messages)-reusable)...)
	for i := reusable; i < len(messages); i++ {
		s.messageShapes[i] = stableReductionMessageShapeOf(&messages[i])
	}
	return s.messageShapes
}

func normalizeMCPMountMessages(messages []message.Message, gitStatus string) []message.Message {
	if strings.TrimSpace(gitStatus) == "" {
		return messages
	}
	prefix := gitStatus + "\n\n"
	normalized := messages
	for i := range messages {
		if messages[i].Role != message.RoleUser {
			continue
		}
		contentPrefixed := strings.HasPrefix(messages[i].Content, prefix)
		injectedPart := len(messages[i].Parts) > 0 &&
			messages[i].Parts[0].Type == message.ContentPartText &&
			messages[i].Parts[0].Text == prefix
		if !contentPrefixed && !injectedPart {
			break
		}
		normalized = append([]message.Message(nil), messages...)
		if contentPrefixed {
			normalized[i].Content = strings.TrimPrefix(normalized[i].Content, prefix)
		}
		if injectedPart {
			normalized[i].Parts = append([]message.ContentPart(nil), normalized[i].Parts[1:]...)
		}
		break
	}
	return normalized
}

func toolDefinitionSignature(def message.ToolDefinition) [sha256.Size]byte {
	data, _ := json.Marshal(def)
	return sha256.Sum256(data)
}

func hasHistoricalMCPToolCall(messages []message.Message, names map[string]struct{}) bool {
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			if _, ok := names[toolpkg.NormalizeName(call.Name)]; ok {
				return true
			}
		}
		for _, item := range msg.ResponsesOutput {
			if item.Type != "function_call" {
				continue
			}
			if _, ok := names[toolpkg.NormalizeName(item.Name)]; ok {
				return true
			}
		}
	}
	return false
}

func sortedToolDefinitions(defs []message.ToolDefinition) []message.ToolDefinition {
	out := append([]message.ToolDefinition(nil), defs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
