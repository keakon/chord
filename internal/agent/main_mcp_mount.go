package agent

import (
	"crypto/sha256"
	"slices"
	"sort"
	"sync"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	toolpkg "github.com/keakon/chord/internal/tools"
)

type mcpToolMountMode uint8

const (
	mcpMountFullInjection mcpToolMountMode = iota
	mcpMountKimiDynamic
	mcpMountResponsesAdditionalTools
	mcpMountAnthropicInlineTools
)

func (m mcpToolMountMode) cacheFriendly() bool {
	return m != mcpMountFullInjection
}

// mcpToolMountMode selects the dynamic mount shape accepted by the currently
// selected target. The mount shape is a property of that target: other pool
// members that do not accept it receive the declarations inlined into the
// top-level tools array by the LLM layer, so pool composition never forces the
// selected target to rebuild its surface.
func (a *MainAgent) mcpToolMountMode() mcpToolMountMode {
	// Deferred schemas use the ordinary top-level contract. Rebuild the
	// request projection when loads, availability, or schema versions change.
	if a.HasDeferredTools() {
		return mcpMountFullInjection
	}
	client, _, selectedRef, _ := a.llmSnapshot()
	if client == nil {
		return mcpMountFullInjection
	}
	if client.SupportsAnthropicInlineTools(selectedRef) {
		return mcpMountAnthropicInlineTools
	}
	if client.SupportsKimiDynamicTools(selectedRef) {
		return mcpMountKimiDynamic
	}
	if client.SupportsResponsesAdditionalTools(selectedRef) {
		return mcpMountResponsesAdditionalTools
	}
	return mcpMountFullInjection
}

// doneToolPermitted reports whether done could be mounted for this role: it is
// registered and no rule denies it. This is deliberately independent of
// whether done is on the current tool surface, because done is mounted only
// while a loop is active — gating loop entry on present visibility would make
// loop mode unreachable. Wildcard-only rules do not deny it (loop mode is the
// authorization); a rule naming done does. See donePermissionAction.
func (a *MainAgent) doneToolPermitted() bool {
	if a == nil || a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get(toolpkg.NameDone); !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) > 0 && normalizeToolPermissionAction(toolpkg.NameDone, donePermissionAction(ruleset)) == permission.ActionDeny {
		return false
	}
	return true
}

// doneOnFrozenToolSurface reports whether done is already part of the frozen
// tool surface the session is currently sending.
func (a *MainAgent) doneOnFrozenToolSurface() bool {
	for _, def := range a.mainLLMToolDefinitions() {
		if def.Name == toolpkg.NameDone {
			return true
		}
	}
	return false
}

// mountDoneForLoopEntry puts done on the tool surface when a loop starts.
// done is absent otherwise (see visibleLLMTools), so loop entry is the only
// place that mounts it. Providers that accept an additional tool mid-session
// get it late-mounted at no prompt-cache cost; everywhere else the tool
// surface is rebuilt, which costs one cache miss that a loop — a long-running
// task by construction — absorbs.
func (a *MainAgent) mountDoneForLoopEntry() {
	if a == nil || !a.doneToolPermitted() || a.doneOnFrozenToolSurface() {
		return
	}
	if a.mcpToolMountMode().cacheFriendly() {
		a.loopDoneLateMount.Store(true)
		return
	}
	a.markRuntimeSurfaceDirty()
}

// loopDoneLateMountDefinition returns done's definition while a loop's
// late-mounted done is currently effective: armed for late mount, the loop
// still authorizes exit, the role still permits it, and the frozen tool
// surface does not already carry it. Any failed condition clears the late
// mount — the declaration snapshot stays anchored in history (prefix
// stability), but done leaves the current effective declaration set, so
// folding targets stop advertising it and the execution gate denies calls.
func (a *MainAgent) loopDoneLateMountDefinition() []message.ToolDefinition {
	if a == nil || !a.loopDoneLateMount.Load() {
		return nil
	}
	if !a.loopExitAuthorized() || !a.doneToolPermitted() || a.doneOnFrozenToolSurface() {
		a.clearLoopDoneLateMount()
		return nil
	}
	tool, ok := a.tools.Get(toolpkg.NameDone)
	if !ok {
		a.clearLoopDoneLateMount()
		return nil
	}
	return llmToolDefinitionsFromVisibleTools([]toolpkg.Tool{tool})
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

// declaredRuntimeMCPToolDefsLocked returns the definitions of the currently
// effective incremental declarations: manual MCP tools outside the top-level
// baseline, plus the done definitions the caller precomputed with
// loopDoneLateMountDefinition. This is the set the request advertises through
// message-level declarations, the set folding targets receive in their
// top-level tools, and the set the native-request tool validation accepts
// calls against. Callers must hold s.mu via the owning agent's mount state;
// doneDefs must be computed before taking that mutex, because its conditions
// read the frozen tool surface, which falls back to this state under the lock.
func (a *MainAgent) declaredRuntimeMCPToolDefsLocked(doneDefs []message.ToolDefinition) []message.ToolDefinition {
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
	return append(llmToolDefinitionsFromVisibleTools(manual), doneDefs...)
}

// effectiveRuntimeMCPDeclarations returns the request's currently effective
// incremental declaration set for targets that fold dynamic declarations into
// their top-level tools. It is a point-in-time snapshot: tools disabled after
// this call stay disabled in later requests because the set is re-read per
// request, and anchored declarations that are no longer effective stay out of
// the set even while their messages still replay in history.
func (a *MainAgent) effectiveRuntimeMCPDeclarations() []message.ToolDefinition {
	doneDefs := a.loopDoneLateMountDefinition()
	a.mcpMountState.mu.Lock()
	defer a.mcpMountState.mu.Unlock()
	return a.declaredRuntimeMCPToolDefsLocked(doneDefs)
}

type mcpMountAnchor struct {
	window     []requestshape.Shape
	occurrence int
}

type mcpMountSnapshot struct {
	anchor   mcpMountAnchor
	tools    []message.ToolDefinition
	overlays []message.Message
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
	messageShapes  []requestshape.Shape
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

// resetMCPMountSurface clears the dynamic-mount bookkeeping and rebuilds the
// runtime tool/prompt surface. Call it at boundaries that rewrite the message
// prefix (durable compaction, session resume, fork) or change the selected
// target (model switch): the next build re-baselines every currently enabled
// manual MCP tool into the top-level tools array — the prompt cache is cold
// there anyway — and enablements after the boundary resume dynamic
// declarations. A tool that was disabled at the boundary and is re-enabled
// afterwards still falls back through the historical-call guard, so a
// declaration never lands after its own calls.
func (a *MainAgent) resetMCPMountSurface() {
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
// original request position. It never emits new declarations: first emission
// happens in mountPendingRuntimeMCPDeclarations once the request's transient
// tail is assembled. If request reduction makes an old anchor
// unresolvable, it falls back to ordinary top-level injection rather than
// moving a declaration behind historical tool calls. The returned positions
// are source-relative indices for every replayed hint and declaration, so
// prompt-cache boundary hints can compensate the mid-list shift.
func (a *MainAgent) mountRuntimeMCPTools(messages []message.Message, mode mcpToolMountMode) ([]message.Message, []message.ToolDefinition, bool, []int) {
	if !mode.cacheFriendly() {
		return messages, nil, false, nil
	}
	// Done's definition conditions read the frozen tool surface, which falls
	// back to the mount state under the lock: precompute before locking.
	doneDefs := a.loopDoneLateMountDefinition()
	state := &a.mcpMountState
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.mode != mode {
		state.mode = mode
		state.snapshots = nil
		state.known = nil
		state.fallback = false
	}
	source, indices := mcpMountSource(messages)
	shapes := state.incrementalMessageShapes(source)
	state.rollbackUnrespondedDeclarations(source, shapes)
	if state.fallback {
		// Once fallback is sticky the rebuilt frozen surface already carries
		// every manual MCP tool (stableVisibleLLMTools returns the full list),
		// so only the late-mounted done still needs top-level defs here.
		return messages, doneDefs, false, nil
	}
	active := a.declaredRuntimeMCPToolDefsLocked(doneDefs)

	// A tool this run has never declared cannot be declared legally when the
	// history already calls it — declared by an earlier run whose anchors were
	// reset (a model switch, or a restored or compacted history) — so fall
	// back to top-level injection instead of anchoring behind its own calls.
	undeclared := make(map[string]struct{}, len(active))
	for _, def := range active {
		name := toolpkg.NormalizeName(def.Name)
		if _, ok := state.known[name]; !ok {
			undeclared[name] = struct{}{}
		}
	}
	if len(undeclared) > 0 && hasHistoricalMCPToolCall(messages, undeclared) {
		state.fallback = true
		return messages, active, true, nil
	}

	positions := make([]int, 0, len(state.snapshots))
	emitted := make([]int, 0, len(state.snapshots))
	lastPosition := 0
	for _, snapshot := range state.snapshots {
		position, ok := findMCPMountAnchor(shapes, snapshot.anchor)
		if !ok || position < lastPosition {
			state.fallback = true
			return messages, active, true, nil
		}
		lastPosition = position
		if position > 0 {
			position = indices[position-1] + 1
		}
		positions = append(positions, position)
	}
	if len(state.snapshots) == 0 {
		return messages, nil, false, nil
	}

	out := make([]message.Message, 0, len(messages)+len(state.snapshots))
	snapshotIndex := 0
	for position := 0; position <= len(messages); position++ {
		for snapshotIndex < len(state.snapshots) && positions[snapshotIndex] == position {
			snapshot := state.snapshots[snapshotIndex]
			out = append(out, snapshot.overlays...)
			for range snapshot.overlays {
				emitted = append(emitted, position)
			}
			out = append(out, message.NewSystemToolsMessage(snapshot.tools))
			emitted = append(emitted, position)
			snapshotIndex++
		}
		if position < len(messages) {
			out = append(out, messages[position])
		}
	}
	return out, nil, false, emitted
}

// rollbackUnrespondedDeclarations drops anchored declaration snapshots no
// model response ever followed. A declaration whose
// request produced no durable assistant output (terminal failure, fully
// rolled-back attempt) leaves nothing after it on the server side: replaying
// it ahead of later user input would place a mid-conversation system group
// where the wire contract requires an assistant, and no thinking exists that
// could still bind to it. The tools re-declare at the next legal tail.
// Callers must hold the mount state mutex; shapes must cover messages.
func (s *mcpToolMountState) rollbackUnrespondedDeclarations(messages []message.Message, shapes []requestshape.Shape) {
	for len(s.snapshots) > 0 {
		last := len(s.snapshots) - 1
		position, ok := findMCPMountAnchor(shapes, s.snapshots[last].anchor)
		if !ok || respondedAfter(messages, position) {
			return
		}
		s.snapshots = s.snapshots[:last]
		s.known = make(map[string][sha256.Size]byte)
		for _, snapshot := range s.snapshots {
			for _, def := range snapshot.tools {
				s.known[toolpkg.NormalizeName(def.Name)] = toolDefinitionSignature(def)
			}
		}
	}
}

// respondedAfter reports whether any durable assistant output follows the
// source position, which pins the declarations anchored at or before it.
func respondedAfter(messages []message.Message, position int) bool {
	for i := max(position, 0); i < len(messages); i++ {
		if messages[i].Role == message.RoleAssistant && !trailingOverlayKind(messages[i].Kind) {
			return true
		}
	}
	return false
}

// mountPendingRuntimeMCPDeclarations first-emits declaration snapshots that
// are still waiting for a legal anchor position. A transient user tail becomes
// part of the declaration snapshot: the next response binds to both, so replay
// must retain the original hints before the declaration. An assistant prefill
// or pending native turn cannot admit a new declaration yet.
func (a *MainAgent) mountPendingRuntimeMCPDeclarations(messages []message.Message, mode mcpToolMountMode) []message.Message {
	if !mode.cacheFriendly() {
		return messages
	}
	// Precompute done's definitions outside the lock; see mountRuntimeMCPTools.
	doneDefs := a.loopDoneLateMountDefinition()
	state := &a.mcpMountState
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.mode != mode || state.fallback {
		return messages
	}
	active := a.declaredRuntimeMCPToolDefsLocked(doneDefs)
	if len(active) == 0 {
		return messages
	}
	if state.known == nil {
		state.known = make(map[string][sha256.Size]byte)
	}
	source, _ := mcpMountSource(messages)
	shapes := state.incrementalMessageShapes(source)
	pending := make([]message.ToolDefinition, 0, len(active))
	for _, def := range active {
		name := toolpkg.NormalizeName(def.Name)
		sig := toolDefinitionSignature(def)
		if previous, ok := state.known[name]; ok && previous == sig {
			continue
		}
		pending = append(pending, def)
	}
	// A deferred emission leaves the tools unknown so the next request
	// boundary re-evaluates them from scratch.
	if len(pending) == 0 || !mountEmissionAllowed(messages, mode) {
		return messages
	}
	for _, def := range pending {
		state.known[toolpkg.NormalizeName(def.Name)] = toolDefinitionSignature(def)
	}
	tail := messages[len(messages)-trailingTurnOverlayCount(messages):]
	overlays := make([]message.Message, 0, len(tail))
	for _, msg := range tail {
		// File snapshots have their own permission-checked replay owner.
		// Retaining a second copy here would bypass its refresh/invalidation.
		if !isCompactionFileSnapshot(msg) {
			overlays = append(overlays, msg)
		}
	}
	state.snapshots = append(state.snapshots, mcpMountSnapshot{
		anchor:   newMCPMountAnchor(shapes),
		tools:    slices.Clone(pending),
		overlays: overlays,
	})
	return append(messages, message.NewSystemToolsMessage(pending))
}

// mcpMountSource uses the same anchor space before and after request projection.
// The indices translate an anchor back into the supplied message list.
func mcpMountSource(messages []message.Message) ([]message.Message, []int) {
	out := make([]message.Message, 0, len(messages))
	indices := make([]int, 0, len(messages))
	for i := range messages {
		msg := &messages[i]
		if msg.Role == message.RoleSystem && len(msg.MCPTools) > 0 {
			continue
		}
		if trailingOverlayKind(msg.Kind) || msg.DiagnosticSource == message.DiagnosticSourceInstructions ||
			msg.Kind == message.KindDirectoryInstructionProjection {
			continue
		}
		out = append(out, *msg)
		indices = append(indices, i)
	}
	return out, indices
}

// trailingOverlayKind reports the request-only message kinds that ride the
// transient tail: turn overlays (runtime hints, pressure notices, compaction
// file snapshots) and the truncated-thinking replay prefix.
func trailingOverlayKind(kind string) bool {
	switch kind {
	case message.KindTurnOverlay, message.KindThinkingReplayPrefix:
		return true
	}
	return false
}

func mountEmissionAllowed(messages []message.Message, mode mcpToolMountMode) bool {
	if len(messages) == 0 || llm.PendingAnthropicNativeTurn(messages) {
		return false
	}
	for i := len(messages) - 1; i >= 0 && trailingOverlayKind(messages[i].Kind); i-- {
		if messages[i].Role != message.RoleUser {
			return false
		}
	}
	last := messages[len(messages)-1]
	return mode != mcpMountAnthropicInlineTools || last.Role == message.RoleUser || last.Role == message.RoleTool
}

const mcpMountAnchorWindow = 3

func newMCPMountAnchor(shapes []requestshape.Shape) mcpMountAnchor {
	windowLen := min(mcpMountAnchorWindow, len(shapes))
	window := make([]requestshape.Shape, windowLen)
	copy(window, shapes[len(shapes)-windowLen:])
	occurrence := 1
	for start := 0; start+windowLen < len(shapes); start++ {
		if mcpMountWindowMatches(shapes, start, window) {
			occurrence++
		}
	}
	return mcpMountAnchor{window: window, occurrence: occurrence}
}

func findMCPMountAnchor(shapes []requestshape.Shape, anchor mcpMountAnchor) (int, bool) {
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

func mcpMountWindowMatches(shapes []requestshape.Shape, start int, window []requestshape.Shape) bool {
	for i, want := range window {
		if shapes[start+i] != want {
			return false
		}
	}
	return true
}

func (s *mcpToolMountState) incrementalMessageShapes(messages []message.Message) []requestshape.Shape {
	reusable := 0
	if len(s.messageSources) == len(s.messageShapes) {
		reusable = requestshape.ReusablePrefixLen(s.messageSources, messages)
		if reusable == len(messages) && len(s.messageSources) == len(messages) {
			return s.messageShapes
		}
	}
	s.messageSources = append(s.messageSources[:reusable], messages[reusable:]...)
	s.messageShapes = append(s.messageShapes[:reusable], make([]requestshape.Shape, len(messages)-reusable)...)
	for i := reusable; i < len(messages); i++ {
		s.messageShapes[i] = requestshape.ShapeOf(&messages[i])
	}
	return s.messageShapes
}

func toolDefinitionSignature(def message.ToolDefinition) [sha256.Size]byte {
	data, _ := jsonx.Marshal(def)
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
