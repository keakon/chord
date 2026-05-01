package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

type visibleStreamTracker struct {
	inner          StreamCallback
	visible        bool
	onVisibleStart func() // called each time a visible streaming attempt begins; may be nil
	providerModel  string
	keySuffix      string
	keyAttempt     int
	keyCount       int
}

func (t *visibleStreamTracker) Callback(delta message.StreamDelta) {
	if t == nil {
		return
	}
	switch delta.Type {
	case "text", "thinking", "tool_use_start", "tool_use_delta", "tool_use_end":
		if !t.visible {
			slog.Debug("llm first visible stream delta",
				"delta_type", delta.Type,
				"model_ref", t.providerModel,
				"key_suffix", t.keySuffix,
				"key_attempt", t.keyAttempt,
				"key_total", t.keyCount,
			)
			t.visible = true
			if t.onVisibleStart != nil {
				t.onVisibleStart()
			}
		}
	case "rollback":
		if t.visible {
			reason := ""
			if delta.Rollback != nil {
				reason = delta.Rollback.Reason
			}
			slog.Debug("llm visible stream rollback",
				"model_ref", t.providerModel,
				"key_suffix", t.keySuffix,
				"key_attempt", t.keyAttempt,
				"key_total", t.keyCount,
				"reason", reason,
			)
		}
		t.visible = false
	}
	if t.inner != nil {
		t.inner(delta)
	}
}

func (t *visibleStreamTracker) EmitRollback(reason string) {
	if t == nil || !t.visible || t.inner == nil {
		return
	}
	t.inner(message.StreamDelta{
		Type: "rollback",
		Rollback: &message.RollbackDelta{
			Reason: reason,
		},
	})
	t.visible = false
}

const maxCoolingWait = 1 * time.Minute

func isAllKeysCoolingError(err error) bool {
	_, ok := errors.AsType[*AllKeysCoolingError](err)
	return ok
}

func shouldContinueRetry(retryCount, maxAttempts int, lastErr error) bool {
	if maxAttempts <= 0 {
		return true
	}
	if retryCount < maxAttempts {
		return true
	}
	return isAllKeysCoolingError(lastErr) || isConcurrentRequestLimit429(lastErr)
}

func clampCoolingWait(wait time.Duration) time.Duration {
	if wait <= 0 {
		return time.Second
	}
	if wait > maxCoolingWait {
		return maxCoolingWait
	}
	return wait
}

func mergeRoundWait(current, candidate time.Duration) time.Duration {
	candidate = clampCoolingWait(candidate)
	if current == 0 || candidate < current {
		return candidate
	}
	return current
}

func roundRetryDelay(backoffDelay, pendingRoundWait time.Duration) time.Duration {
	if backoffDelay <= 0 {
		return pendingRoundWait
	}
	if pendingRoundWait > 0 && pendingRoundWait < backoffDelay {
		return pendingRoundWait
	}
	return backoffDelay
}

func nextRetryCount(current int, roundHadUsableReply bool) int {
	if roundHadUsableReply {
		return 0
	}
	return current + 1
}

func responseHasUsableOutput(resp *message.Response) bool {
	if resp == nil {
		return false
	}
	return strings.TrimSpace(resp.Content) != "" || len(resp.ToolCalls) > 0 || len(resp.ThinkingBlocks) > 0
}

// completeStreamWithRetry walks the model pool (cursor-start entry + optional
// remaining entries) and, for each model, loops over keys until success, a permanent failure, or
// keys exhausted. Retriable API errors rotate keys; 401/403 force key rotation
// (OAuth refresh when possible). Response-phase net timeouts also rotate keys
// before advancing to the next model (isPerKeyTimeoutRetry). Dial/connect and
// TLS handshake timeouts do not rotate keys and skip sibling targets on that
// provider for the current round. Exponential backoff applies between full
// rounds, not between keys.
func (c *Client) completeStreamWithRetry(
	ctx context.Context,
	startProvider *ProviderConfig,
	startImpl Provider,
	startModelID string,
	startMaxTokens int,
	startTuning RequestTuning,
	startVariant string,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
	maxAttempts int,
	status *CallStatus,
) (*message.Response, error) {
	var lastErr error
	var pendingRoundWait time.Duration
	abortIfCancelled := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("LLM request aborted: %w", err)
		}
		return nil
	}

	if maxAttempts <= 0 {
		maxAttempts = 0
	}

	lastInputTokens := c.getLastInputTokens()
	outputCapSetting := c.getOutputTokenMax()
	systemPrompt := c.getSystemPrompt()
	variantForStart := strings.TrimSpace(startVariant)

	retryCount := 0

	// 默认 public stream path 使用无限轮重试；若显式传入 maxAttempts>0，则按
	// shouldContinueRetry 的规则收敛。AllKeysCooling/部分 429 仍可超出显式上限。
	for round := 0; shouldContinueRetry(retryCount, maxAttempts, lastErr); round++ {
		// skippedProviders only applies within the current round. Connection-
		// establishment failures should skip sibling targets on the same provider
		// for this round, but the next round must probe the provider again.
		skippedProviders := map[*ProviderConfig]bool{}
		if err := abortIfCancelled(); err != nil {
			return nil, err
		}
		// Apply backoff delay only between full retry rounds.
		if round > 0 {
			delay := roundRetryDelay(startProvider.GetRetryDelay(retryCount), pendingRoundWait)
			slog.Info("retrying LLM request round",
				"attempt", round+1,
				"retry_count", retryCount,
				"delay", delay,
				"last_error", lastErr,
			)
			if cb != nil {
				if err := abortIfCancelled(); err != nil {
					return nil, err
				}
				detail := fmt.Sprintf("round %d", round+1)
				cb(message.StreamDelta{
					Type: "status",
					Status: &message.StatusDelta{
						Type:   "retrying",
						Detail: detail,
					},
				})
			}
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
				}
			}
		}
		pendingRoundWait = 0
		roundHadUsableReply := false

		// Define the list of models to try in this round.
		// Models are tried in order: current cursor-start entry first, then the
		// remaining pool entries. This implements sticky-cursor failover: the
		// current successful entry stays pinned until it fails.
		type target struct {
			provider     *ProviderConfig
			impl         Provider
			modelID      string
			maxTokens    int
			contextLimit int
			tuning       RequestTuning
			variant      string
			isFallback   bool
		}

		targets := []target{
			{
				provider:     startProvider,
				impl:         startImpl,
				modelID:      startModelID,
				maxTokens:    startMaxTokens,
				contextLimit: c.ContextLimitForModelRef(providerModelRef(startProvider, startModelID)),
				tuning:       startTuning,
				variant:      variantForStart,
				isFallback:   false,
			},
		}

		// Inject the remaining model-pool entries when enabled. All models in the
		// pool are tried in order within each attempt round.
		if fallbackEnabled {
			for _, fb := range fallbackModels {
				var fbTuning RequestTuning
				fbVariantUsed := ""
				if m, ok := fb.ProviderConfig.GetModel(fb.ModelID); ok {
					fbTuning = tuningFromModel(m)
					if fb.Variant != "" {
						if v, ok := m.Variants[fb.Variant]; ok {
							fbTuning = mergeVariantTuning(fbTuning, v)
							fbVariantUsed = fb.Variant
						}
					}
				}
				targets = append(targets, target{
					provider:     fb.ProviderConfig,
					impl:         fb.ProviderImpl,
					modelID:      fb.ModelID,
					maxTokens:    fb.MaxTokens,
					contextLimit: fb.ContextLimit,
					tuning:       fbTuning,
					variant:      fbVariantUsed,
					isFallback:   true,
				})
			}
		}

		for ti := 0; ti < len(targets); ti++ {
			if err := abortIfCancelled(); err != nil {
				return nil, err
			}
			t := targets[ti]
			if skippedProviders[t.provider] {
				slog.Info("skipping model: provider unreachable (dial/DNS/connect error)",
					"model", t.modelID,
					"provider", t.provider.Name(),
				)
				continue
			}
			if t.isFallback {
				slog.Info("trying fallback model in retry rotation",
					"model", t.modelID,
					"attempt", round+1,
				)
				if cb != nil {
					reason := ""
					if status != nil {
						reason = status.FallbackReason
					}
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
					displayRef := providerModelRef(t.provider, t.modelID)
					if t.variant != "" {
						displayRef = displayRef + "@" + t.variant
					}
					cb(message.StreamDelta{
						Type: "status",
						Status: &message.StatusDelta{
							Type:     "retrying",
							Detail:   fmt.Sprintf("fallback: %s", t.modelID),
							ModelRef: displayRef,
							Reason:   reason,
						},
					})
				}
				if status != nil {
					status.FallbackTriggered = true
				}
			}

			// Try all keys for this model. Loop until a request succeeds,
			// a non-key error occurs, or all keys are exhausted (AllKeysCoolingError).
			keyCount := t.provider.KeyCount()
			if keyCount == 0 {
				keyCount = 1
			}
			targetMessages := messages
			effectiveMaxTokens := t.maxTokens
			if m, ok := t.provider.GetModel(t.modelID); ok {
				effectiveMaxTokens = clampEffectiveMaxTokens(
					m,
					effectiveMaxTokens,
					outputCapSetting,
					t.tuning,
					systemPrompt,
					targetMessages,
					tools,
					lastInputTokens,
				)
			}
			var apiKey string
			var resp *message.Response
			var err error
			modelDone := false
			for keyAttempt := 0; keyAttempt < keyCount; keyAttempt++ {
				if err := abortIfCancelled(); err != nil {
					return nil, err
				}
				var keySwitched bool
				apiKey, keySwitched, err = t.provider.SelectKeyWithContext(ctx)
				if err := abortIfCancelled(); err != nil {
					return nil, err
				}
				if keySwitched {
					t.provider.ClearInlineDisplayRateLimitSnapshot()
					if cb != nil {
						cb(message.StreamDelta{Type: "key_switched"})
					}
					t.provider.WakeCodexRateLimitPolling()
				}
				if err != nil {
					if _, ok := errors.AsType[*NoUsableKeysError](err); ok {
						lastErr = err
						break // exit key loop, move to next model
					}
					// AllKeysCoolingError: all keys exhausted for this model.
					if cooling, ok := errors.AsType[*AllKeysCoolingError](err); ok {
						lastErr = err
						pendingRoundWait = mergeRoundWait(pendingRoundWait, cooling.RetryAfter)
						if ti < len(targets)-1 {
							// Fallback target available: skip to it immediately without waiting.
							slog.Info("all API keys cooling; trying next model",
								"model", t.modelID,
								"provider", t.provider.Name(),
							)
						} else {
							// Last target: wait only between rounds.
							wait := pendingRoundWait
							slog.Info("all API keys cooling; waiting before retry",
								"model", t.modelID,
								"provider", t.provider.Name(),
								"attempt", round+1,
								"retry_after", wait,
							)
							if cb != nil {
								if err := abortIfCancelled(); err != nil {
									return nil, err
								}
								cb(message.StreamDelta{
									Type: "status",
									Status: &message.StatusDelta{
										Type:   "cooling",
										Detail: wait.Round(time.Second).String(),
									},
								})
							}
						}
					} else {
						lastErr = err
					}
					break // exit key loop, move to next model
				}

				// Make the request with this key.
				// Context size pre-check and token budget are computed once per target
				// and reused across key retries in the same round.
				if modelDone {
					break
				}

				slog.Debug("LLM request",
					"model", t.modelID,
					"key_suffix", keySuffix(apiKey),
					"max_tokens", effectiveMaxTokens,
				)
				modelRef := providerModelRef(t.provider, t.modelID)
				if t.variant != "" {
					modelRef = modelRef + "@" + t.variant
				}
				keySuffixValue := keySuffix(apiKey)
				attemptReason := ""
				if t.isFallback && status != nil {
					attemptReason = status.FallbackReason
				}
				tracker := &visibleStreamTracker{
					inner:         cb,
					providerModel: modelRef,
					keySuffix:     keySuffixValue,
					keyAttempt:    keyAttempt + 1,
					keyCount:      keyCount,
					onVisibleStart: func() {
						t.provider.MarkKeySuccess(apiKey)
						slog.Debug("llm emitting streaming status after visible output",
							"model_ref", modelRef,
							"key_suffix", keySuffixValue,
							"key_attempt", keyAttempt+1,
							"key_total", keyCount,
						)
						if cb != nil {
							cb(message.StreamDelta{
								Type:   "status",
								Status: &message.StatusDelta{Type: "streaming"},
							})
							// key_confirmed must carry the effective model ref so the
							// agent/UI can confirm routing decisions (fallback/key switch)
							// only after the model actually begins emitting visible output.
							cb(message.StreamDelta{
								Type: "key_confirmed",
								Status: &message.StatusDelta{
									ModelRef: modelRef,
									Reason:   attemptReason,
								},
							})
						}
					},
				}
				resp, err = t.impl.CompleteStream(
					ctx,
					apiKey,
					t.modelID,
					systemPrompt,
					targetMessages,
					tools,
					effectiveMaxTokens,
					t.tuning,
					tracker.Callback,
				)
				if err == nil {
					if responseHasUsableOutput(resp) {
						roundHadUsableReply = true
					}
					t.provider.MarkKeySuccess(apiKey)
					t.provider.WakeCodexRateLimitPolling()
					inputTok, outputTok := 0, 0
					if resp.Usage != nil {
						inputTok = resp.Usage.InputTokens
						outputTok = resp.Usage.OutputTokens
						lastInputTokens = resp.Usage.InputTokens
						c.setLastInputTokens(resp.Usage.InputTokens)
					}
					slog.Debug("LLM request completed",
						"model", t.modelID,
						"input_tokens", inputTok,
						"output_tokens", outputTok,
						"stop_reason", resp.StopReason,
					)
					if status != nil {
						status.RunningModelRef = providerModelRef(t.provider, t.modelID)
						if t.variant != "" {
							status.RunningModelRef = status.RunningModelRef + "@" + t.variant
						}
						status.RunningContextLimit = t.contextLimit
					}
					modelDone = true
					break // success: exit key loop
				}

				// Turn cancelled (e.g. Ctrl+C): do not rotate keys, advance models, or backoff.
				if ctx.Err() != nil {
					return nil, fmt.Errorf("LLM request aborted: %w", ctx.Err())
				}

				lastErr = err
				visibleStarted := tracker.visible
				tracker.EmitRollback(err.Error())
				if err := abortIfCancelled(); err != nil {
					return nil, err
				}
				if !visibleStarted {
					cooldownResult := markKeyCooldown(ctx, t.provider, apiKey, lastErr)
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
					if (cooldownResult.deactivatedAccountID != "" || cooldownResult.deactivatedEmail != "") && cb != nil {
						cb(message.StreamDelta{Type: "key_deactivated", AccountID: cooldownResult.deactivatedAccountID, Email: cooldownResult.deactivatedEmail})
					}
					cooldownApplied := cooldownResult.cooldownApplied
					keyForRotationCooldown := apiKey
					if cooldownResult.refreshedKey != "" {
						keyForRotationCooldown = cooldownResult.refreshedKey
					}
					if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
						status.FallbackReason = classifyFallbackReason(err)
					}

					retriable := isRetriable(err)
					fallbackEligible := shouldFallback(err)
					// 401/403 are treated as per-key failures so selection can prefer other
					// healthy keys before giving up or switching model.
					var apiErrPtr *APIError
					if errors.As(err, &apiErrPtr) && apiErrPtr != nil && (apiErrPtr.StatusCode == 401 || apiErrPtr.StatusCode == 403) {
						retriable = true
					}
					// Response-phase timeouts before any visible output: try other keys on
					// this model first. Connection-establishment timeouts (dial/connect/TLS
					// handshake) skip keys and move to the next model/provider.
					if !retriable && fallbackEligible && keyAttempt+1 < keyCount && isPerKeyTimeoutRetry(err) {
						retriable = true
					}

					if !retriable {
						slog.Error("non-retriable LLM error",
							"model", t.modelID,
							"key_suffix", keySuffix(apiKey),
							"error", err,
						)
						if fallbackEligible && fallbackEnabled && len(fallbackModels) > 0 {
							// fallback-eligible: move on to next model in pool
							modelDone = true
							break
						}
						modelDone = true // stop trying other keys for this model
						break
					}

					if !cooldownApplied && keyForRotationCooldown != "" {
						t.provider.MarkRecovering(keyForRotationCooldown)
					}

					slog.Warn("retriable LLM error, trying next key",
						"model", t.modelID,
						"key_suffix", keySuffix(apiKey),
						"error", err,
					)
					if cb != nil && keyAttempt+1 < keyCount {
						if err := abortIfCancelled(); err != nil {
							return nil, err
						}
						cb(message.StreamDelta{
							Type: "status",
							Status: &message.StatusDelta{
								Type:   "retrying_key",
								Detail: fmt.Sprintf("%d/%d", keyAttempt+2, keyCount),
							},
						})
					}
					continue
				}

				if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
					status.FallbackReason = classifyFallbackReason(err)
				}
				slog.Warn("stream interrupted after visible output; retrying current key",
					"model", t.modelID,
					"key_suffix", keySuffix(apiKey),
					"error", err,
				)
				if cb != nil {
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
					cb(message.StreamDelta{
						Type: "status",
						Status: &message.StatusDelta{
							Type:   "retrying",
							Detail: "same key",
						},
					})
				}
				continue
			} // end key loop
			// All keys exhausted with retriable errors: mark done so the targets
			// loop moves on to the next model rather than waiting for next attempt.
			if !modelDone {
				modelDone = true
			}
			// Dial/connect failures and connection-establishment timeouts skip all
			// remaining models on this provider (same endpoint for every key).
			if skipRemainingModelsOnProvider(lastErr) {
				skippedProviders[t.provider] = true
			}

			// If request succeeded, check for semantically empty output before returning.
			// A response with no content, no tool calls, and no thinking blocks means
			// this key produced nothing useful. Mark it recovering so future selection
			// prefers other healthy keys, then let the current round continue through the
			// remaining target/model chain.
			if resp != nil && err == nil {
				if !responseHasUsableOutput(resp) {
					emptyErr := error(&EmptyResponseError{})
					if resp.StopReason == "length" || resp.StopReason == "max_tokens" {
						emptyErr = &EmptyTruncationError{}
					}
					slog.Warn("model returned empty response, trying next key",
						"model", t.modelID,
						"provider", t.provider.Name(),
						"stop_reason", resp.StopReason,
						"key_suffix", keySuffix(apiKey),
					)
					lastErr = emptyErr
					t.provider.MarkRecovering(apiKey)
					resp = nil
					if cb != nil {
						cb(message.StreamDelta{
							Type: "status",
							Status: &message.StatusDelta{
								Type:   "retrying_key",
								Detail: "next",
							},
						})
					}
					continue
				}
				return resp, nil
			}
			// Stop immediately only for permanent failures (auth, permission, malformed request).
			if modelDone && isPermanentFailure(lastErr) {
				slog.Error("LLM permanent failure, giving up",
					"model", startModelID,
					"error", lastErr,
				)
				return nil, lastErr
			}
		} // end targets loop
		// Track that the full model pool was tried and all failed.
		if fallbackEnabled && status != nil && status.FallbackTriggered {
			status.FallbackExhausted = true
		}
		if _, ok := errors.AsType[*NoUsableKeysError](lastErr); ok {
			return nil, lastErr
		}
		if isTerminalModelPoolFailure(lastErr) {
			return nil, lastErr
		}
		retryCount = nextRetryCount(retryCount, roundHadUsableReply)
	} // end attempts loop

	slog.Error("LLM all retries exhausted",
		"max_attempts", maxAttempts,
		"model", startModelID,
		"error", lastErr,
	)
	return nil, lastErr
}

// estimateInputTokens provides a rough token estimate from messages when no
// API-reported usage is available (e.g. the cursor-start model failed on every attempt).
// Uses the heuristic of ~3 characters per token (conservative for English).
func estimateInputTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		n := len(msg.Content)
		for _, tc := range msg.ToolCalls {
			n += len(tc.Args)
		}
		for _, tb := range msg.ThinkingBlocks {
			n += len(tb.Thinking)
		}
		total += n
	}
	if total == 0 {
		return 0
	}
	return total / 3
}

func estimateToolDefinitionTokens(tools []message.ToolDefinition) int {
	if len(tools) == 0 {
		return 0
	}
	total := 0
	for _, tool := range tools {
		n := len(tool.Name) + len(tool.Description)
		if schemaBytes, err := json.Marshal(tool.InputSchema); err == nil {
			n += len(schemaBytes)
		}
		total += n
	}
	if total == 0 {
		return 0
	}
	return total / 3
}

func estimateRequestInputTokens(systemPrompt string, messages []message.Message, tools []message.ToolDefinition) int {
	total := estimateInputTokens(messages) + estimateToolDefinitionTokens(tools)
	if systemPrompt != "" {
		total += len(systemPrompt) / 3
	}
	if total < 1 {
		return 1
	}
	return total
}

// EstimateRequestInputTokens returns the same rough request-side token estimate
// used by clampEffectiveMaxTokens. It includes system prompt, messages, and
// tool schema overhead.
func EstimateRequestInputTokens(systemPrompt string, messages []message.Message, tools []message.ToolDefinition) int {
	return estimateRequestInputTokens(systemPrompt, messages, tools)
}

func clampEffectiveMaxTokens(
	model config.ModelConfig,
	effectiveMaxTokens int,
	outputCapSetting int,
	tuning RequestTuning,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	lastInputTokens int,
) int {
	if model.Limit.Output > 0 && model.Limit.Output < effectiveMaxTokens {
		effectiveMaxTokens = model.Limit.Output
	}
	if tuning.OpenAI.ReasoningEffort == "" {
		outputCap := outputCapSetting
		if outputCap <= 0 {
			outputCap = DefaultOutputTokenMax
		}
		if outputCap < effectiveMaxTokens {
			minForThinking := 0
			if tuning.Anthropic.ThinkingBudget > 0 {
				minForThinking = tuning.Anthropic.ThinkingBudget + 1024
			}
			if outputCap < minForThinking {
				outputCap = minForThinking
			}
			if outputCap < effectiveMaxTokens {
				effectiveMaxTokens = outputCap
			}
		}
	}
	if model.Limit.Context > 0 {
		inputEstimate := estimateRequestInputTokens(systemPrompt, messages, tools)
		if lastInputTokens > inputEstimate {
			inputEstimate = lastInputTokens
		}
		buffer := max(model.Limit.Context/100, 256)
		contextCap := max(model.Limit.Context-inputEstimate-buffer, 1)
		if contextCap < effectiveMaxTokens {
			effectiveMaxTokens = contextCap
		}
	}
	return effectiveMaxTokens
}
