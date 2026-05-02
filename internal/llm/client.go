package llm

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// DefaultOutputTokenMax is the global cap on requested output tokens.
// Tool-heavy or reasoning flows often need more; 32K reduces truncation
// while still avoiding context overflow. Users can override via
// max_output_tokens in config.
const DefaultOutputTokenMax = 32000

// DefaultStreamRetryRounds is retained for explicit bounded retry calls in
// tests/internal helpers. The public streaming path now defaults to infinite
// round retries, with the round backoff still capped at 1 minute.
const DefaultStreamRetryRounds = 3

// FallbackModel describes an entry in the ordered model pool used by the
// client. Requests start from the current sticky cursor and only advance to the
// next entry when the current entry fails.
type FallbackModel struct {
	ProviderConfig *ProviderConfig
	ProviderImpl   Provider
	ModelID        string
	MaxTokens      int
	ContextLimit   int    // from ModelConfig.Limit.Context
	Variant        string // named variant to apply; empty = use model defaults
}

// Client is the high-level LLM client that handles retries and key selection.
type Client struct {
	mu              sync.RWMutex
	provider        *ProviderConfig
	providerImpl    Provider
	modelID         string
	maxTokens       int
	outputTokenMax  int // global output token cap (Layer 1); 0 means use DefaultOutputTokenMax
	tuning          RequestTuning
	nextTuning      *RequestTuning
	activeVariant   string // name of the currently applied variant (empty = none)
	systemPrompt    string
	lastInputTokens int             // tracks last known input token count for context size checks
	fallbackModels  []FallbackModel // ordered list of remaining model-pool entries after the current cursor head
	poolCursor      int             // sticky cursor over the effective model pool; success pins, failure advances
	lastCallStatus  CallStatus
}

// CallStatus describes the effective model-routing outcome of the most recent
// CompleteStream call.
type CallStatus struct {
	SelectedModelRef    string
	RunningModelRef     string
	RunningContextLimit int
	FallbackTriggered   bool
	FallbackReason      string
	FallbackExhausted   bool
}

// NewClient creates a new Client for making LLM completions.
func NewClient(
	providerCfg *ProviderConfig,
	providerImpl Provider,
	modelID string,
	maxTokens int,
	systemPrompt string,
) *Client {
	var tuning RequestTuning
	if m, ok := providerCfg.GetModel(modelID); ok {
		tuning = tuningFromModel(m)
	}

	return &Client{
		provider:     providerCfg,
		providerImpl: providerImpl,
		modelID:      modelID,
		maxTokens:    maxTokens,
		tuning:       tuning,
		systemPrompt: systemPrompt,
		lastCallStatus: CallStatus{
			SelectedModelRef: providerModelRef(providerCfg, modelID),
			RunningModelRef:  providerModelRef(providerCfg, modelID),
		},
	}
}

// SetSystemPrompt updates the system prompt used for subsequent completions.
func (c *Client) SetSystemPrompt(prompt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.systemPrompt = prompt
}

// SetFallbackModels sets the ordered tail of the model pool after the current
// head entry. Requests stay pinned to the current successful entry and only
// advance into this tail when the current entry fails.
func (c *Client) SetFallbackModels(models []FallbackModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallbackModels = append([]FallbackModel(nil), models...)
	c.poolCursor = 0
}

// SetModelPool configures the client with an ordered model pool.
// selectedIdx identifies the initial sticky cursor position; requests start
// there, wrap through the remaining entries on failure, and remain pinned to
// the entry that most recently succeeded.
func (c *Client) SetModelPool(models []FallbackModel, selectedIdx int) {
	if len(models) == 0 {
		return
	}
	if selectedIdx < 0 || selectedIdx >= len(models) {
		selectedIdx = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sel := models[selectedIdx]
	c.provider = sel.ProviderConfig
	c.providerImpl = sel.ProviderImpl
	c.modelID = sel.ModelID
	c.maxTokens = sel.MaxTokens
	c.tuning = RequestTuning{}
	c.activeVariant = ""
	if m, ok := sel.ProviderConfig.GetModel(sel.ModelID); ok {
		c.tuning = tuningFromModel(m)
		if sel.Variant != "" {
			if v, ok := m.Variants[sel.Variant]; ok {
				c.tuning = mergeVariantTuning(c.tuning, v)
			}
			c.activeVariant = sel.Variant
		}
	}
	fallbacks := make([]FallbackModel, 0, len(models)-1)
	for i := 1; i < len(models); i++ {
		fallbacks = append(fallbacks, models[(selectedIdx+i)%len(models)])
	}
	c.fallbackModels = fallbacks
	c.poolCursor = 0
}

// ProviderConfig returns the bound provider config.
func (c *Client) ProviderConfig() *ProviderConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider
}

// ModelID returns the model id of the current cursor head bound to this client.
func (c *Client) ModelID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.modelID
}

// MaxTokens returns the configured max output tokens before dynamic capping.
func (c *Client) MaxTokens() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxTokens
}

// OutputTokenMax returns the configured global output cap.
func (c *Client) OutputTokenMax() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.outputTokenMax
}

// Variant returns the active variant name.
func (c *Client) Variant() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activeVariant
}

// SetOutputTokenMax sets the global output token cap (Layer 1). If n is 0,
// DefaultOutputTokenMax is used. This cap is applied before the dynamic
// context-aware capping (Layer 2) in completeStreamWithRetry.
func (c *Client) SetOutputTokenMax(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputTokenMax = n
}

// SetVariant applies the named variant from the current cursor head model's Variants map.
// If the variant name is empty or not found, it is a no-op.
// Variant fields (Thinking, Reasoning) override the model-level defaults.
func (c *Client) SetVariant(variantName string) {
	if variantName == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.provider.GetModel(c.modelID)
	if !ok {
		return
	}
	v, ok := m.Variants[variantName]
	if !ok {
		return
	}
	base := tuningFromModel(m)
	c.tuning = mergeVariantTuning(base, v)
	c.activeVariant = variantName
}

// SetNextRequestTuningOverride applies a one-shot tuning override to the next
// Complete or CompleteStream call only. It is intended for runtime recovery or
// other per-request constraints that must not mutate the client's long-lived
// model/variant defaults.
func (c *Client) SetNextRequestTuningOverride(tuning RequestTuning) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := tuning
	copy.OpenAI.ParallelToolCalls = cloneBoolPtr(tuning.OpenAI.ParallelToolCalls)
	c.nextTuning = &copy
}

// consumeRequestTuningOverrideLocked must be called with c.mu held.
func (c *Client) consumeRequestTuningOverrideLocked() (RequestTuning, bool) {
	if c.nextTuning == nil {
		return RequestTuning{}, false
	}
	override := *c.nextTuning
	override.OpenAI.ParallelToolCalls = cloneBoolPtr(c.nextTuning.OpenAI.ParallelToolCalls)
	c.nextTuning = nil
	return override, true
}

// ActiveVariant returns the name of the currently applied variant, or empty string if none.
func (c *Client) ActiveVariant() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activeVariant
}

// LastCallStatus returns a copy of the most recent call status.
func (c *Client) LastCallStatus() CallStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastCallStatus
}

// PrimaryModelRef returns the current cursor-head provider/model reference.
func (c *Client) PrimaryModelRef() string {
	return providerModelRef(c.provider, c.modelID)
}

// RunningModelRef returns the most recent effective running provider/model.
func (c *Client) RunningModelRef() string {
	st := c.LastCallStatus()
	if st.RunningModelRef != "" {
		return st.RunningModelRef
	}
	return st.SelectedModelRef
}

// ContextLimitForModelRef returns the configured context window for a
// provider/model ref in this client's effective model pool.
// Returns 0 when unknown.
func (c *Client) ContextLimitForModelRef(ref string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.contextLimitForModelRefLocked(ref)
}

// ThinkingToolcallCompat returns compatibility config for the active model.
// Returns nil when the model has no compat block configured.
func (c *Client) ThinkingToolcallCompat() *config.ThinkingToolcallCompatConfig {
	if c == nil || c.provider == nil {
		return nil
	}
	return c.provider.ThinkingToolcallCompat(c.modelID)
}

// SupportsInput reports whether the current model accepts the given input modality.
func (c *Client) SupportsInput(modality string) bool {
	if c == nil || c.provider == nil {
		return modality == "text" || modality == "image"
	}
	m, ok := c.provider.GetModel(c.modelID)
	if !ok {
		return modality == "text" || modality == "image"
	}
	return m.SupportsInput(modality)
}

// CompleteStream sends a streaming completion request with automatic retries.
// Routing uses completeStreamWithRetry: within each model, keys are tried
// according to isRetriable/shouldFallback plus overrides (401/403, response-
// phase timeouts via isPerKeyTimeoutRetry). Connection-establishment timeouts
// skip remaining keys on the provider and may skip other models on the same
// provider (skipRemainingModelsOnProvider).
//
// Retries happen in full rounds with exponential backoff between rounds; each
// round walks the model pool and each model's selectable keys.
//
// If additional model-pool entries are configured and the current cursor-head
// model fails with a fallback-eligible error after exhausting keys (and any
// model-level retries), subsequent pool entries are tried in order within the
// same round.
func (c *Client) CompleteStream(
	ctx context.Context,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
) (*message.Response, error) {
	c.mu.Lock()
	pool := c.modelPoolLocked()
	startIdx := c.poolCursor
	if startIdx < 0 || startIdx >= len(pool) {
		startIdx = 0
		c.poolCursor = 0
	}
	start := pool[startIdx]
	orderedFallbacks := rotatePoolAfterStart(pool, startIdx)
	requestTuning := tuningForPoolTarget(start)
	if override, ok := c.consumeRequestTuningOverrideLocked(); ok {
		requestTuning = override
	}
	startRef := modelRefWithVariant(start)
	startLimit := start.ContextLimit
	if startLimit <= 0 && start.ProviderConfig != nil {
		if m, ok := start.ProviderConfig.GetModel(start.ModelID); ok {
			startLimit = m.Limit.Context
		}
	}
	c.mu.Unlock()

	status := CallStatus{
		SelectedModelRef:    startRef,
		RunningModelRef:     startRef,
		RunningContextLimit: startLimit,
	}

	// Single call handles the whole round-based retry chain (cursor-start entry
	// + remaining pool). AllKeysCoolingError records the shortest wait seen in
	// the round, continues through remaining targets, then waits only between rounds.
	resp, err := c.completeStreamWithRetry(
		ctx, start.ProviderConfig, start.ProviderImpl, start.ModelID,
		start.MaxTokens, requestTuning, start.Variant,
		messages, tools, cb, true, orderedFallbacks, 0, &status,
	)

	c.mu.Lock()
	switch {
	case err == nil:
		if idx := findPoolIndexByRef(pool, status.RunningModelRef); idx >= 0 {
			c.poolCursor = idx
		} else {
			c.poolCursor = startIdx
		}
	case errorsIsContextCanceled(err):
	// User cancellation should not move the sticky model cursor.
	default:
		if len(pool) > 1 {
			c.poolCursor = (startIdx + 1) % len(pool)
		}
	}
	if status.SelectedModelRef == "" {
		status.SelectedModelRef = startRef
	}
	if status.RunningModelRef == "" {
		status.RunningModelRef = status.SelectedModelRef
	}
	c.lastCallStatus = status
	c.mu.Unlock()
	return resp, err
}

func (c *Client) setLastInputTokens(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastInputTokens = n
}

// providerForModelRefLocked must be called with c.mu held (read or write).
func (c *Client) providerForModelRefLocked(ref string) *ProviderConfig {
	normalizedRef := strings.TrimSpace(ref)
	if normalizedRef != "" {
		normalizedRef, _ = config.ParseModelRef(normalizedRef)
	}
	if c.provider != nil {
		if normalizedRef == "" || normalizedRef == providerModelRef(c.provider, c.modelID) {
			return c.provider
		}
	}
	for _, fb := range c.fallbackModels {
		if fb.ProviderConfig == nil {
			continue
		}
		if normalizedRef == providerModelRef(fb.ProviderConfig, fb.ModelID) {
			return fb.ProviderConfig
		}
	}
	return c.provider
}

// contextLimitForModelRefLocked must be called with c.mu held (read or write).
func (c *Client) contextLimitForModelRefLocked(ref string) int {
	normalizedRef := strings.TrimSpace(ref)
	if normalizedRef != "" {
		normalizedRef, _ = config.ParseModelRef(normalizedRef)
	}
	if c.provider != nil {
		primaryRef := providerModelRef(c.provider, c.modelID)
		if normalizedRef == "" || normalizedRef == primaryRef {
			if m, ok := c.provider.GetModel(c.modelID); ok {
				return m.Limit.Context
			}
		}
	}
	for _, fb := range c.fallbackModels {
		if fb.ProviderConfig == nil {
			continue
		}
		if normalizedRef == providerModelRef(fb.ProviderConfig, fb.ModelID) {
			if fb.ContextLimit > 0 {
				return fb.ContextLimit
			}
			if m, ok := fb.ProviderConfig.GetModel(fb.ModelID); ok {
				return m.Limit.Context
			}
			return 0
		}
	}
	return 0
}

func (c *Client) getLastInputTokens() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastInputTokens
}

func (c *Client) getOutputTokenMax() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.outputTokenMax
}

func (c *Client) getSystemPrompt() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.systemPrompt
}

// modelPoolLocked returns the effective model pool with the current cursor head
// as the first entry followed by the remaining configured entries. Caller must hold c.mu.
func (c *Client) modelPoolLocked() []FallbackModel {
	pool := make([]FallbackModel, 0, 1+len(c.fallbackModels))
	primaryLimit := 0
	if c.provider != nil {
		if m, ok := c.provider.GetModel(c.modelID); ok {
			primaryLimit = m.Limit.Context
		}
	}
	pool = append(pool, FallbackModel{
		ProviderConfig: c.provider,
		ProviderImpl:   c.providerImpl,
		ModelID:        c.modelID,
		MaxTokens:      c.maxTokens,
		ContextLimit:   primaryLimit,
		Variant:        c.activeVariant,
	})
	pool = append(pool, c.fallbackModels...)
	return pool
}

func rotatePoolAfterStart(pool []FallbackModel, start int) []FallbackModel {
	if len(pool) <= 1 {
		return nil
	}
	if start < 0 || start >= len(pool) {
		start = 0
	}
	out := make([]FallbackModel, 0, len(pool)-1)
	for i := 1; i < len(pool); i++ {
		out = append(out, pool[(start+i)%len(pool)])
	}
	return out
}

func tuningForPoolTarget(t FallbackModel) RequestTuning {
	if t.ProviderConfig == nil {
		return RequestTuning{}
	}
	m, ok := t.ProviderConfig.GetModel(t.ModelID)
	if !ok {
		return RequestTuning{}
	}
	base := tuningFromModel(m)
	if t.Variant == "" {
		return base
	}
	if v, ok := m.Variants[t.Variant]; ok {
		return mergeVariantTuning(base, v)
	}
	return base
}

func modelRefWithVariant(t FallbackModel) string {
	if t.ProviderConfig == nil {
		return ""
	}
	ref := providerModelRef(t.ProviderConfig, t.ModelID)
	if t.Variant != "" {
		ref += "@" + t.Variant
	}
	return ref
}

func findPoolIndexByRef(pool []FallbackModel, runningRef string) int {
	normalizedRunning := strings.TrimSpace(runningRef)
	if normalizedRunning == "" {
		return -1
	}
	for i, target := range pool {
		if modelRefWithVariant(target) == normalizedRunning {
			return i
		}
		base, _ := config.ParseModelRef(modelRefWithVariant(target))
		runningBase, _ := config.ParseModelRef(normalizedRunning)
		if base != "" && base == runningBase {
			return i
		}
	}
	return -1
}

func errorsIsContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

func (c *Client) ProviderForModelRefForTest(ref string) *ProviderConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.providerForModelRefLocked(ref)
}

func providerModelRef(provider *ProviderConfig, modelID string) string {
	if provider == nil {
		return modelID
	}
	name := strings.TrimSpace(provider.Name())
	if name == "" {
		return modelID
	}
	return name + "/" + modelID
}
