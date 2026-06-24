package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
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
	ProviderConfig   *ProviderConfig
	ProviderImpl     Provider
	ModelID          string
	MaxTokens        int
	ContextLimit     int    // from ModelConfig.Limit.Context
	InputLimit       int    // fixed input-side override; when zero the client derives the current budget from model limits/output cap
	DeriveInputLimit bool   // when true, re-derive InputLimit from the current output cap and use InputLimit only as a fallback cache
	Variant          string // named variant to apply; empty or undefined = use model defaults
}

// Client is the high-level LLM client that handles retries and key selection.
type Client struct {
	mu                     sync.RWMutex
	provider               *ProviderConfig
	providerImpl           Provider
	modelID                string
	maxTokens              int
	outputTokenMax         int // global output token cap (Layer 1); 0 means use DefaultOutputTokenMax
	streamRetryRounds      int // hard cap on public CompleteStream retry rounds; 0 means retry until success/cancel
	terminalAPIStatusCodes map[int]struct{}
	tuning                 RequestTuning
	nextTuning             *RequestTuning
	activeVariant          string // name of the currently applied variant (empty = none)
	systemPrompt           string
	lastInputTokens        int             // tracks last known input token count for context size checks
	fallbackModels         []FallbackModel // ordered list of remaining model-pool entries after the current cursor head
	poolCursor             int             // sticky cursor over the effective model pool; success pins, failure advances
	toolSurfacePrimary     FallbackModel   // first entry of the effective model pool; defines stable modality-dependent tool visibility
	lastCallStatus         CallStatus
	serviceTier            config.ServiceTier

	routingGeneration atomic.Uint64
	routingChangedCh  chan struct{}

	codexWarmupStarted bool
	codexWarmupCancel  context.CancelFunc
}

// CallStatus describes the effective model-routing outcome of the most recent
// CompleteStream call.
type CallStatus struct {
	SelectedModelRef    string
	RunningModelRef     string
	RunningContextLimit int
	RunningInputLimit   int
	FallbackTriggered   bool
	FallbackReason      string
	FallbackExhausted   bool
	ServiceTier         config.ServiceTier
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
		tuning = tuningFromModel(m, providerCfg.Preset(), providerCfg.SupportedServiceTiers())
	}

	c := &Client{
		provider:         providerCfg,
		providerImpl:     providerImpl,
		modelID:          modelID,
		maxTokens:        maxTokens,
		tuning:           tuning,
		systemPrompt:     systemPrompt,
		routingChangedCh: make(chan struct{}),
		lastCallStatus: CallStatus{
			SelectedModelRef: providerModelRef(providerCfg, modelID),
			RunningModelRef:  providerModelRef(providerCfg, modelID),
		},
	}
	c.toolSurfacePrimary = c.PrimaryModelEntry()
	c.startCodexWarmup()
	return c
}

// RoutingInvalidatedError indicates the current retry/fallback plan became stale
// because model routing changed while a request was in progress.
type RoutingInvalidatedError struct {
	StartedGeneration uint64
	CurrentGeneration uint64
}

func (e *RoutingInvalidatedError) Error() string {
	return fmt.Sprintf("llm routing invalidated: started_generation=%d current_generation=%d", e.StartedGeneration, e.CurrentGeneration)
}

// IsRoutingInvalidated reports whether err means the request should abandon the
// current retry/fallback plan and start a fresh request using the latest routing.
func IsRoutingInvalidated(err error) bool {
	_, ok := errors.AsType[*RoutingInvalidatedError](err)
	return ok
}

// InvalidateRouting marks the current retry/fallback plan stale for all future
// retry boundaries and resets provider-specific incremental transport chains.
func (c *Client) InvalidateRouting(reason string) {
	if c == nil {
		return
	}
	var warmupCancel context.CancelFunc
	c.mu.Lock()
	providers := c.providersLocked()
	prevCh := c.routingChangedCh
	if c.codexWarmupCancel != nil {
		warmupCancel = c.codexWarmupCancel
		c.codexWarmupCancel = nil
	}
	c.routingGeneration.Add(1)
	c.routingChangedCh = make(chan struct{})
	c.mu.Unlock()
	if prevCh != nil {
		close(prevCh)
	}
	if warmupCancel != nil {
		warmupCancel()
	}
	for _, p := range providers {
		if invalidator, ok := p.(routingInvalidator); ok {
			invalidator.InvalidateRouting(reason)
		}
	}
}

func (c *Client) startCodexWarmup() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.codexWarmupStarted {
		c.mu.Unlock()
		return
	}
	provider := c.provider
	c.mu.Unlock()
	if provider == nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.codexWarmupStarted || c.provider == nil {
		c.mu.Unlock()
		cancel()
		return
	}
	c.codexWarmupStarted = true
	c.codexWarmupCancel = cancel
	c.mu.Unlock()

	if !provider.StartCodexWarmup(ctx) {
		cancel()
		c.mu.Lock()
		c.codexWarmupCancel = nil
		c.codexWarmupStarted = false
		c.mu.Unlock()
	}
}

func (c *Client) routingSnapshot() (generation uint64, changed <-chan struct{}) {
	if c == nil {
		return 0, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	changed = c.routingChangedCh
	generation = c.routingGeneration.Load()
	return
}

func (c *Client) routingInvalidated(startGeneration uint64) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	current := c.routingGeneration.Load()
	return current, current != startGeneration
}

func (c *Client) providersLocked() []Provider {
	providers := make([]Provider, 0, 1+len(c.fallbackModels))
	if c.providerImpl != nil {
		providers = append(providers, c.providerImpl)
	}
	for _, fb := range c.fallbackModels {
		if fb.ProviderImpl != nil {
			providers = append(providers, fb.ProviderImpl)
		}
	}
	return providers
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
	c.fallbackModels = sanitizeFallbackModelVariants(models)
	c.poolCursor = 0
	c.toolSurfacePrimary = c.primaryModelEntryLocked()
}

func validVariantForModel(provider *ProviderConfig, modelID, variant string) string {
	variant = strings.TrimSpace(variant)
	if provider == nil || strings.TrimSpace(modelID) == "" || variant == "" {
		return ""
	}
	m, ok := provider.GetModel(modelID)
	if !ok {
		return ""
	}
	if _, ok := m.Variants[variant]; !ok {
		return ""
	}
	return variant
}

func sanitizeFallbackModelVariant(model FallbackModel) FallbackModel {
	model.Variant = validVariantForModel(model.ProviderConfig, model.ModelID, model.Variant)
	return model
}

func sanitizeFallbackModelVariants(models []FallbackModel) []FallbackModel {
	out := make([]FallbackModel, 0, len(models))
	for _, model := range models {
		out = append(out, sanitizeFallbackModelVariant(model))
	}
	return out
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
	models = sanitizeFallbackModelVariants(models)
	sel := models[selectedIdx]
	c.toolSurfacePrimary = models[0]
	c.provider = sel.ProviderConfig
	c.providerImpl = sel.ProviderImpl
	c.modelID = sel.ModelID
	c.maxTokens = sel.MaxTokens
	c.tuning = RequestTuning{}
	c.activeVariant = ""
	if m, ok := sel.ProviderConfig.GetModel(sel.ModelID); ok {
		c.tuning = tuningFromModel(m, sel.ProviderConfig.Preset(), sel.ProviderConfig.SupportedServiceTiers())
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

// PrimaryModelEntry returns the current cursor-head entry as a reusable fallback-model
// descriptor. Callers can use it to assemble a separate model pool while
// preserving provider implementations and model limits.
func (c *Client) PrimaryModelEntry() FallbackModel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.primaryModelEntryLocked()
}

func (c *Client) primaryModelEntryLocked() FallbackModel {
	entry := FallbackModel{
		ProviderConfig: c.provider,
		ProviderImpl:   c.providerImpl,
		ModelID:        c.modelID,
		MaxTokens:      c.maxTokens,
		Variant:        validVariantForModel(c.provider, c.modelID, c.activeVariant),
	}
	if c.provider != nil {
		if m, ok := c.provider.GetModel(c.modelID); ok {
			entry.ContextLimit = m.Limit.Context
			entry.InputLimit = m.Limit.EffectiveInputBudget(c.outputTokenMax, DefaultOutputTokenMax)
		}
	}
	return entry
}

// ModelPoolSnapshot returns the effective model pool and the sticky cursor index.
// The returned pool is ordered the same way CompleteStream traverses it; callers
// can pass the pair to SetModelPool on a separate client to preserve routing state
// without sharing request-local mutations.
func (c *Client) ModelPoolSnapshot() ([]FallbackModel, int) {
	if c == nil {
		return nil, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	pool := c.modelPoolLocked()
	cursor := c.poolCursor
	if cursor < 0 || cursor >= len(pool) {
		cursor = 0
	}
	return pool, cursor
}

// NextRequestModelRef returns the provider/model ref that the next request will
// start from, including an inline variant suffix when the cursor entry has one.
func (c *Client) NextRequestModelRef() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	pool := c.modelPoolLocked()
	cursor := c.poolCursor
	if cursor < 0 || cursor >= len(pool) {
		cursor = 0
	}
	if len(pool) == 0 {
		return ""
	}
	return modelRefWithVariant(pool[cursor])
}

// SetOutputTokenMax sets the global output token cap (Layer 1). If n is 0,
// DefaultOutputTokenMax is used. This cap is applied before the dynamic
// context-aware capping (Layer 2) in completeStreamWithRetry.
func (c *Client) SetOutputTokenMax(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputTokenMax = n
}

// SetStreamRetryRounds sets a hard cap on public CompleteStream retry rounds.
// Values <= 0 keep the default behavior of retrying full rounds until success,
// cancellation, or a non-retriable terminal failure.
func (c *Client) SetStreamRetryRounds(n int) {
	if n < 0 {
		n = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamRetryRounds = n
}

// SetTerminalAPIStatusCodes marks API status codes as terminal for this client.
// Terminal errors still run per-key bookkeeping (for example OAuth refresh-token
// expiry/deactivation marking) before returning; a successful OAuth refresh is
// treated as remediation and may still be retried. Normal runtime clients leave
// this unset; diagnostic callers can use it to avoid retrying deterministic
// client/auth failures.
func (c *Client) SetTerminalAPIStatusCodes(statusCodes ...int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(statusCodes) == 0 {
		c.terminalAPIStatusCodes = nil
		return
	}
	codes := make(map[int]struct{}, len(statusCodes))
	for _, code := range statusCodes {
		if code > 0 {
			codes[code] = struct{}{}
		}
	}
	if len(codes) == 0 {
		c.terminalAPIStatusCodes = nil
		return
	}
	c.terminalAPIStatusCodes = codes
}

func (c *Client) isTerminalAPIStatusError(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok = c.terminalAPIStatusCodes[apiErr.StatusCode]
	return ok
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
	base := tuningFromModel(m, c.provider.Preset(), c.provider.SupportedServiceTiers())
	c.tuning = mergeVariantTuning(base, v)
	c.activeVariant = variantName
}

// SetNextRequestTuningOverride applies one-shot tuning fields to the next
// Complete or CompleteStream call only. The fields are merged over the
// model/variant defaults so request-local constraints do not erase defaults such
// as reasoning effort or text verbosity.
func (c *Client) SetNextRequestTuningOverride(tuning RequestTuning) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := cloneRequestTuning(tuning)
	c.nextTuning = &copy
}

// MergeNextRequestTuningOverride merges tuning fields into the next request's
// one-shot override instead of replacing it. Non-zero/non-empty fields in tuning
// win over the existing pending override.
func (c *Client) MergeNextRequestTuningOverride(tuning RequestTuning) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nextTuning == nil {
		copy := cloneRequestTuning(tuning)
		c.nextTuning = &copy
		return
	}
	merged := mergeRequestTuning(*c.nextTuning, tuning)
	c.nextTuning = &merged
}

// consumeRequestTuningOverrideLocked must be called with c.mu held.
func (c *Client) consumeRequestTuningOverrideLocked() (RequestTuning, bool) {
	if c.nextTuning == nil {
		return RequestTuning{}, false
	}
	override := cloneRequestTuning(*c.nextTuning)
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

// NoteRunningModelRef records the model ref currently attempted by an in-flight
// streaming request. It lets status surfaces reflect fallback routing before the
// request completes and LastCallStatus is replaced by the final call status.
func (c *Client) NoteRunningModelRef(ref string) {
	ref = strings.TrimSpace(ref)
	if c == nil || ref == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prevRef := strings.TrimSpace(c.lastCallStatus.RunningModelRef)
	c.lastCallStatus.RunningModelRef = ref
	if c.lastCallStatus.SelectedModelRef == "" {
		c.lastCallStatus.SelectedModelRef = providerModelRef(c.provider, c.modelID)
	}
	if ref != prevRef || c.lastCallStatus.RunningContextLimit <= 0 {
		c.lastCallStatus.RunningContextLimit = c.contextLimitForModelRefLocked(ref)
	}
	if ref != prevRef || c.lastCallStatus.RunningInputLimit <= 0 {
		c.lastCallStatus.RunningInputLimit = c.inputLimitForModelRefLocked(ref)
	}
}

// ServiceTier returns the configured runtime request tier for this client.
func (c *Client) ServiceTier() config.ServiceTier {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return config.NormalizeServiceTier(string(c.serviceTier))
}

func supportedServiceTiersForTuning(tuning RequestTuning) []config.ServiceTier {
	tiers := []config.ServiceTier{config.ServiceTierStandard}
	for _, tier := range []config.ServiceTier{config.ServiceTierFast, config.ServiceTierSlow} {
		if tuning.SupportedServiceTiers[tier] {
			tiers = append(tiers, tier)
		}
	}
	return tiers
}

func effectiveServiceTierForTuning(tuning RequestTuning, tier config.ServiceTier) config.ServiceTier {
	tier = config.NormalizeServiceTier(string(tier))
	if tier == config.ServiceTierStandard || !tuning.SupportedServiceTiers[tier] {
		return config.ServiceTierStandard
	}
	return tier
}

// SupportedServiceTiersForModelRef returns the user-selectable tiers accepted by
// the configured model ref in the effective model pool. Standard is always
// included because it means omitting any non-standard provider tier hint.
func (c *Client) SupportedServiceTiersForModelRef(ref string) []config.ServiceTier {
	if c == nil {
		return []config.ServiceTier{config.ServiceTierStandard}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.supportedServiceTiersForModelRefLocked(ref)
}

func (c *Client) supportedServiceTiersForModelRefLocked(ref string) []config.ServiceTier {
	normalizedRef := strings.TrimSpace(ref)
	if normalizedRef != "" {
		normalizedRef, _ = config.ParseModelRef(normalizedRef)
	}
	if c.provider != nil {
		primaryRef := providerModelRef(c.provider, c.modelID)
		if normalizedRef == "" || normalizedRef == primaryRef {
			return supportedServiceTiersForTuning(tuningForPoolTarget(FallbackModel{ProviderConfig: c.provider, ModelID: c.modelID, Variant: c.activeVariant}))
		}
	}
	for _, fb := range c.fallbackModels {
		if fb.ProviderConfig == nil {
			continue
		}
		if normalizedRef == providerModelRef(fb.ProviderConfig, fb.ModelID) {
			return supportedServiceTiersForTuning(tuningForPoolTarget(fb))
		}
	}
	return []config.ServiceTier{config.ServiceTierStandard}
}

// EffectiveServiceTierForModelRef returns the tier that can actually be applied
// to the configured model ref in the effective model pool.
func (c *Client) EffectiveServiceTierForModelRef(ref string) config.ServiceTier {
	if c == nil {
		return config.ServiceTierStandard
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.effectiveServiceTierForModelRefLocked(ref)
}

func (c *Client) effectiveServiceTierForModelRefLocked(ref string) config.ServiceTier {
	tier := config.NormalizeServiceTier(string(c.serviceTier))
	if tier == config.ServiceTierStandard {
		return config.ServiceTierStandard
	}
	normalizedRef := strings.TrimSpace(ref)
	if normalizedRef != "" {
		normalizedRef, _ = config.ParseModelRef(normalizedRef)
	}
	if c.provider != nil {
		primaryRef := providerModelRef(c.provider, c.modelID)
		if normalizedRef == "" || normalizedRef == primaryRef {
			return effectiveServiceTierForTuning(tuningForPoolTarget(FallbackModel{ProviderConfig: c.provider, ModelID: c.modelID, Variant: c.activeVariant}), tier)
		}
	}
	for _, fb := range c.fallbackModels {
		if fb.ProviderConfig == nil {
			continue
		}
		if normalizedRef == providerModelRef(fb.ProviderConfig, fb.ModelID) {
			return effectiveServiceTierForTuning(tuningForPoolTarget(fb), tier)
		}
	}
	return config.ServiceTierStandard
}

// SetServiceTier sets the runtime request tier. It affects subsequent LLM requests,
// including later retry rounds that have not yet built their request targets.
func (c *Client) SetServiceTier(tier config.ServiceTier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serviceTier = config.NormalizeServiceTier(string(tier))
}

// ContextLimitForModelRef returns the configured context window for a
// provider/model ref in this client's effective model pool.
// Returns 0 when unknown.
func (c *Client) ContextLimitForModelRef(ref string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.contextLimitForModelRefLocked(ref)
}

// InputLimitForModelRef returns the input-side token budget for a provider/model
// ref in this client's effective model pool. When limit.input is not configured,
// it derives the budget from limit.context minus the effective max output.
func (c *Client) InputLimitForModelRef(ref string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.inputLimitForModelRefLocked(ref)
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

func (c *Client) SupportsToolResultModalities(modalities []string) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fallbackModelCanReplayToolResultModalities(FallbackModel{ProviderConfig: c.provider, ModelID: c.modelID}, modalities)
}

// PrimarySupportsViewImageTool reports whether the configured primary model can
// expose view_image. The primary entry, not the sticky fallback cursor, defines
// the stable tool surface for the session.
func (c *Client) PrimarySupportsViewImageTool() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fallbackModelCanReplayImageToolResults(c.toolSurfacePrimary)
}

func fallbackModelCanReplayImageToolResults(model FallbackModel) bool {
	return fallbackModelCanReplayToolResultModalities(model, []string{"image"})
}

func fallbackModelCanReplayToolResultModalities(model FallbackModel, modalities []string) bool {
	if model.ProviderConfig == nil {
		return false
	}
	m, ok := model.ProviderConfig.GetModel(model.ModelID)
	if !ok {
		return false
	}
	for _, modality := range modalities {
		if !m.SupportsInput(modality) {
			return false
		}
	}
	switch providerWireFamily(model.ProviderConfig) {
	case modelcompat.WireFamilyOpenAIChat:
		return false
	default:
		return true
	}
}

func streamTargetCanReplayToolResultModalities(target streamRetryTarget, modalities []string) bool {
	return fallbackModelCanReplayToolResultModalities(FallbackModel{
		ProviderConfig: target.provider,
		ModelID:        target.modelID,
	}, modalities)
}

func requiredToolResultModalities(messages []message.Message) []string {
	var needsImage, needsPDF bool
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, part := range msg.Parts {
			switch part.Type {
			case "image":
				needsImage = true
			case "pdf":
				needsPDF = true
			}
		}
	}
	modalities := make([]string, 0, 2)
	if needsImage {
		modalities = append(modalities, "image")
	}
	if needsPDF {
		modalities = append(modalities, "pdf")
	}
	return modalities
}

// CompleteStream sends a streaming completion request with automatic retries.
// Routing uses completeStreamWithRetry: within each model, keys are tried
// according to isRetriable/shouldFallback plus overrides (currently 401/403).
// Timeouts before any visible output skip remaining keys on the provider and
// may skip other models on the same provider (skipRemainingModelsOnProvider);
// visible stream interruptions retry on the same key.
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
		requestTuning = mergeRequestTuning(requestTuning, override)
	}
	startRef := modelRefWithVariant(start)
	startLimit := start.ContextLimit
	startInputLimit := resolveFallbackInputLimit(start, c.outputTokenMax)
	if startLimit <= 0 && start.ProviderConfig != nil {
		if m, ok := start.ProviderConfig.GetModel(start.ModelID); ok {
			startLimit = m.Limit.Context
		}
	}
	if startInputLimit <= 0 {
		startInputLimit = startLimit
	}
	wireMessages := messages
	routingGeneration := c.routingGeneration.Load()
	routingChangedCh := c.routingChangedCh
	streamRetryRounds := c.streamRetryRounds
	serviceTier := c.serviceTier
	c.mu.Unlock()

	status := CallStatus{
		SelectedModelRef:    startRef,
		RunningModelRef:     startRef,
		RunningContextLimit: startLimit,
		RunningInputLimit:   startInputLimit,
		ServiceTier:         effectiveServiceTierForTuning(requestTuning, serviceTier),
	}

	// Single call handles the whole round-based retry chain (cursor-start entry
	// + remaining pool). AllKeysCoolingError records the shortest wait seen in
	// the round, continues through remaining targets, then waits only between rounds.
	maxAttempts := 0
	if streamRetryRounds > 0 {
		maxAttempts = -streamRetryRounds
	}
	resp, err := c.completeStreamWithRetry(
		ctx, start.ProviderConfig, start.ProviderImpl, start.ModelID,
		start.MaxTokens, requestTuning, start.Variant,
		wireMessages, tools, cb, true, orderedFallbacks, maxAttempts, &status,
		routingGeneration, routingChangedCh,
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

// inputLimitForModelRefLocked must be called with c.mu held (read or write).
func (c *Client) inputLimitForModelRefLocked(ref string) int {
	normalizedRef := strings.TrimSpace(ref)
	if normalizedRef != "" {
		normalizedRef, _ = config.ParseModelRef(normalizedRef)
	}
	if c.provider != nil {
		primaryRef := providerModelRef(c.provider, c.modelID)
		if normalizedRef == "" || normalizedRef == primaryRef {
			if m, ok := c.provider.GetModel(c.modelID); ok {
				return m.Limit.EffectiveInputBudget(c.outputTokenMax, DefaultOutputTokenMax)
			}
		}
	}
	for _, fb := range c.fallbackModels {
		if fb.ProviderConfig == nil {
			continue
		}
		if normalizedRef == providerModelRef(fb.ProviderConfig, fb.ModelID) {
			return resolveFallbackInputLimit(fb, c.outputTokenMax)
		}
	}
	return 0
}

func resolveFallbackInputLimit(target FallbackModel, outputTokenMax int) int {
	if target.InputLimit > 0 && !target.DeriveInputLimit {
		return target.InputLimit
	}
	if target.ProviderConfig != nil {
		if m, ok := target.ProviderConfig.GetModel(target.ModelID); ok {
			if budget := m.Limit.EffectiveInputBudget(outputTokenMax, DefaultOutputTokenMax); budget > 0 {
				return budget
			}
		}
	}
	if target.InputLimit > 0 {
		return target.InputLimit
	}
	if target.ContextLimit > 0 {
		return target.ContextLimit
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
	primaryInputLimit := 0
	if c.provider != nil {
		if m, ok := c.provider.GetModel(c.modelID); ok {
			primaryLimit = m.Limit.Context
			primaryInputLimit = m.Limit.EffectiveInputBudget(c.outputTokenMax, DefaultOutputTokenMax)
		}
	}
	pool = append(pool, FallbackModel{
		ProviderConfig: c.provider,
		ProviderImpl:   c.providerImpl,
		ModelID:        c.modelID,
		MaxTokens:      c.maxTokens,
		ContextLimit:   primaryLimit,
		InputLimit:     primaryInputLimit,
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

func normalizeMessagesForPoolTarget(msgs []message.Message, target FallbackModel, tuning RequestTuning) ([]message.Message, modelcompat.NormalizeReport) {
	if target.ProviderConfig == nil {
		return msgs, modelcompat.NormalizeReport{}
	}
	modelRef := providerModelRef(target.ProviderConfig, target.ModelID)
	variant := validVariantForModel(target.ProviderConfig, target.ModelID, target.Variant)
	if variant != "" {
		modelRef = modelRef + "@" + variant
	}
	tm := modelcompat.TargetModel{
		ProviderID:              target.ProviderConfig.Name(),
		ModelID:                 target.ModelID,
		Variant:                 variant,
		ModelRef:                modelRef,
		WireFamily:              providerWireFamily(target.ProviderConfig),
		ThinkingReplayEnabled:   thinkingReplayEnabled(target.ProviderConfig, target.ModelID, tuning),
		ToolResultEncoding:      toolResultEncoding(target.ProviderConfig),
		SupportsStructuredTools: supportsStructuredTools(target.ProviderConfig),
	}
	return modelcompat.NormalizeForTarget(msgs, tm, modelcompat.NormalizeOptions{StructuredTools: true})
}

func providerWireFamily(provider *ProviderConfig) string {
	if provider == nil {
		return modelcompat.WireFamilyUnknown
	}
	switch provider.Type() {
	case config.ProviderTypeMessages:
		return modelcompat.WireFamilyAnthropic
	case config.ProviderTypeChatCompletions:
		return modelcompat.WireFamilyOpenAIChat
	case config.ProviderTypeResponses:
		return modelcompat.WireFamilyOpenAIResponses
	case config.ProviderTypeGenerateContent:
		return modelcompat.WireFamilyGemini
	default:
		return modelcompat.WireFamilyUnknown
	}
}

func toolResultEncoding(provider *ProviderConfig) string {
	switch providerWireFamily(provider) {
	case modelcompat.WireFamilyAnthropic:
		return modelcompat.ToolResultEncodingAnthropicUserBlock
	case modelcompat.WireFamilyOpenAIChat, modelcompat.WireFamilyOpenAIResponses:
		return modelcompat.ToolResultEncodingOpenAIToolRole
	case modelcompat.WireFamilyGemini:
		return modelcompat.ToolResultEncodingGeminiUserParts
	default:
		return modelcompat.ToolResultEncodingNone
	}
}

func supportsStructuredTools(provider *ProviderConfig) bool {
	switch providerWireFamily(provider) {
	case modelcompat.WireFamilyAnthropic, modelcompat.WireFamilyOpenAIChat, modelcompat.WireFamilyOpenAIResponses, modelcompat.WireFamilyGemini:
		return true
	default:
		return false
	}
}

func thinkingReplayEnabled(provider *ProviderConfig, modelID string, tuning RequestTuning) bool {
	if providerWireFamily(provider) != modelcompat.WireFamilyAnthropic {
		return false
	}
	if tuning.Anthropic.ThinkingType == "enabled" || tuning.Anthropic.ThinkingType == "adaptive" {
		return true
	}
	if provider == nil {
		return false
	}
	m, ok := provider.GetModel(modelID)
	if !ok {
		return false
	}
	typeName := m.EffectiveThinkingType()
	return typeName == "enabled" || typeName == "adaptive"
}

func tuningForPoolTarget(t FallbackModel) RequestTuning {
	if t.ProviderConfig == nil {
		return RequestTuning{}
	}
	m, ok := t.ProviderConfig.GetModel(t.ModelID)
	if !ok {
		return RequestTuning{}
	}
	base := tuningFromModel(m, t.ProviderConfig.Preset(), t.ProviderConfig.SupportedServiceTiers())
	variant := validVariantForModel(t.ProviderConfig, t.ModelID, t.Variant)
	if variant == "" {
		return base
	}
	if v, ok := m.Variants[variant]; ok {
		return mergeVariantTuning(base, v)
	}
	return base
}

func modelRefWithVariant(t FallbackModel) string {
	if t.ProviderConfig == nil {
		return ""
	}
	ref := providerModelRef(t.ProviderConfig, t.ModelID)
	if variant := validVariantForModel(t.ProviderConfig, t.ModelID, t.Variant); variant != "" {
		ref += "@" + variant
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
