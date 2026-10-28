package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	randv2 "math/rand/v2"
	"net/http"
	"time"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
)

const (
	geminiMaxRecoveryAttempts      = 8
	geminiMaxSameCheckpointRetries = 3
	geminiMaxPhysicalRequests      = 128
	geminiRecoveryWaitBudget       = 120 * time.Second
)

type geminiRecoveryPolicy struct {
	jitter func(time.Duration) time.Duration
	wait   func(context.Context, time.Duration) error
}

func defaultGeminiRecoveryPolicy() geminiRecoveryPolicy {
	return geminiRecoveryPolicy{jitter: func(base time.Duration) time.Duration { return time.Duration(randv2.Int64N(int64(base) + 1)) }, wait: func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-executiongate.FromContext(ctx).Frozen():
			return &executiongate.ErrSessionPaused{}
		case <-timer.C:
			return nil
		}
	}}
}

func (g *GeminiProvider) completeStreamWithContinuation(ctx context.Context, frozen geminiFrozenRequest, cb StreamCallback) (*message.Response, error) {
	return g.runGeminiContinuation(ctx, frozen, cb, defaultGeminiRecoveryPolicy())
}

func (g *GeminiProvider) runGeminiContinuation(ctx context.Context, frozen geminiFrozenRequest, cb StreamCallback, policy geminiRecoveryPolicy) (*message.Response, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	chain := hex.EncodeToString(id[:])
	state := &geminiDecodeState{cb: cb}
	ledger := geminiUsageLedger{known: true}
	reported := false
	accepted := false
	recoveries, samePoint := 0, 0
	var waited time.Duration
	abandon := func(reason string, cause error) (*message.Response, error) {
		if cb != nil {
			cb(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Scope: message.RollbackScopeRequest}})
		}
		return nil, &GeminiContinuationError{Reason: reason, Cause: cause}
	}
	interrupted := func(err error) (*message.Response, error) {
		state.RestoreCheckpoint(cb)
		state.finish = "interrupted"
		response, _ := state.Materialize()
		response.ToolCalls = nil
		response.GeminiParts = nil
		response.ReasoningContent = ""
		response.Usage = ledger.usage()
		response.UsageReported = reported
		return response, err
	}
	for request := 1; ; request++ {
		if err := ctx.Err(); err != nil {
			return interrupted(err)
		}
		if err := executiongate.Check(ctx); err != nil {
			return interrupted(err)
		}
		if request > geminiMaxPhysicalRequests {
			return abandon("request_limit", nil)
		}
		token := state.checkpoint.Token
		body, err := geminiBodyWithContinuation(frozen.body, token)
		if err != nil {
			return abandon("protocol_error", err)
		}
		state.finish = ""
		state.latestUsage = nil
		state.hopCheckpointUsage = nil
		state.hopCheckpointUsageComplete = false
		state.hopCheckpoint = false
		state.terminalUsage = false
		if token != "" {
			diagnosticServerContextReuse(ctx)
		}
		result := g.streamGeminiHop(ctx, frozen, body, state, cb)
		if executiongate.IsPaused(result.Err) {
			if err := ctx.Err(); err != nil {
				return interrupted(err)
			}
			return interrupted(result.Err)
		}
		accepted = accepted || result.Accepted
		outcome := "missing"
		if result.Usage != nil {
			outcome = "checkpoint"
			if result.Err == nil && result.FinishReason != "" {
				outcome = "completed"
			}
		}
		// An unaccepted request has no confirmed output and cannot invalidate window observations.
		if result.Accepted && (state.hopCheckpoint || (result.Err == nil && result.FinishReason != "")) {
			ledger.settle(result.Usage, token == "", result.UsageComplete)
			if state.usageInvalid {
				ledger.known = false
			}
		}
		reported = reportGeminiContinuationUsage(ctx, g.provider, chain, request, outcome, result.usageState(), result.Usage) || reported
		if err := ctx.Err(); err != nil {
			return interrupted(err)
		}
		if result.Err == nil && result.FinishReason != "" && result.FinishReason != geminiContinuationFinish {
			response, err := state.Materialize()
			response.Usage = ledger.usage()
			response.UsageReported = reported
			if err != nil {
				return response, err
			}
			if state.thinking && cb != nil {
				cb(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
			}
			if err := ctx.Err(); err != nil {
				return interrupted(err)
			}
			if gateErr := executiongate.Check(ctx); gateErr != nil {
				return response, gateErr
			}
			publishGeminiFinalTools(response, cb)
			return response, nil
		}
		if err := executiongate.Check(ctx); err != nil {
			return interrupted(err)
		}
		// Before the first accepted response there is no generation to recover.
		// Let the client apply its ordinary key rotation and error classification.
		if !accepted && token == "" && result.Err != nil {
			return nil, result.Err
		}
		if state.checkpoint.Token != token {
			samePoint = 0
		}
		if result.Err == nil && result.FinishReason == geminiContinuationFinish {
			if state.checkpoint.Token == "" || state.checkpoint.Token == token {
				return abandon("no_progress", nil)
			}
			continue
		}
		if continuationErr, ok := errors.AsType[*GeminiContinuationError](result.Err); ok {
			return abandon(continuationErr.Reason, continuationErr.Cause)
		}
		if apiErr, ok := errors.AsType[*APIError](result.Err); ok {
			switch apiErr.StatusCode {
			case 0, 408, 429, 500, 502, 503, 504:
			default:
				return abandon("token_rejected", apiErr)
			}
		}
		state.RestoreCheckpoint(cb)
		if recoveries >= geminiMaxRecoveryAttempts || samePoint >= geminiMaxSameCheckpointRetries {
			return abandon("recovery_exhausted", result.Err)
		}
		delay := policy.jitter(time.Second * time.Duration(1<<samePoint))
		if apiErr, ok := errors.AsType[*APIError](result.Err); ok {
			delay = max(delay, apiErr.RetryAfter)
		}
		if delay > geminiRecoveryWaitBudget-waited {
			return abandon("recovery_exhausted", result.Err)
		}
		if err := policy.wait(ctx, delay); err != nil {
			return interrupted(err)
		}
		waited += delay
		recoveries++
		samePoint++
	}
}

func freezeGeminiRequest(g *GeminiProvider, model, key string, body []byte) geminiFrozenRequest {
	headers := make(http.Header)
	headers.Set(headerContentType, headerValueApplicationJSON)
	headers.Set("x-goog-api-key", key)
	setProviderLLMUserAgent(headers, g.provider, "")
	overrides := g.provider.RequestOverrides(model)
	applyRequestHeaderOverrides(headers, overrides)
	return geminiFrozenRequest{overrides: overrides, model: model, url: geminiStreamURL(g.provider.APIURL(), model), headers: headers, body: body}
}
