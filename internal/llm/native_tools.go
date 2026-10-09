package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// NativeToolPolicy is supplied only by an agent request. Auxiliary requests
// cannot inherit authorization from a model's configuration.
type NativeToolPolicy struct {
	Permitted func(string) bool
	Preflight func(context.Context) error
	Failed    func(*message.NativeToolHistory)
	Begin     func(context.Context, NativeRequestRecord) (string, error)
	Finish    func(string, message.NativeRequestOutcome, *message.Response, error) error
}

type NativeRequestRecord struct {
	Tuning        RequestTuning                   `json:"tuning"`
	ServiceTier   config.ServiceTier              `json:"service_tier"`
	Target        string                          `json:"target"`
	Protocol      string                          `json:"protocol"`
	APIURL        string                          `json:"api_url"`
	Authorization message.NativeToolAuthorization `json:"authorization"`
	System        string                          `json:"system"`
	Messages      []message.Message               `json:"messages"`
	Tools         []message.ToolDefinition        `json:"tools"`
	MaxTokens     int                             `json:"max_tokens"`
	Continuation  int                             `json:"continuation"`
}

type nativePolicyContextKey struct{}

// NativeToolError is terminal for automatic retry/fallback: the request may
// already have executed and incurred charges, or durable authorization failed.
type NativeToolError struct {
	Cause   error
	Receipt *message.NativeToolHistory
}

func (e *NativeToolError) Error() string {
	return "native tool request stopped; automatic replay is disabled: " + e.Cause.Error()
}
func (e *NativeToolError) Unwrap() error { return e.Cause }

func IsNativeToolError(err error) bool { _, ok := errors.AsType[*NativeToolError](err); return ok }

func completeWithNativeTools(ctx context.Context, impl Provider, provider *ProviderConfig, key, model, system string, messages []message.Message, defs []message.ToolDefinition, maxTokens int, tuning RequestTuning, serviceTier config.ServiceTier, cb StreamCallback) (_ *message.Response, retErr error) {
	if err := validateNativeClientToolReplay(messages); err != nil {
		return nil, &NativeToolError{Cause: err}
	}
	target := provider.Name() + "/" + model
	pending := make(map[string]bool)
	var pendingAuthorization message.NativeToolAuthorization
	for _, msg := range messages {
		if native := msg.NativeTools; native != nil && (native.Target != target || native.Protocol != provider.Type() || native.APIURL != provider.APIURL()) {
			return nil, &NativeToolError{Cause: fmt.Errorf("native history belongs to %s; use that target or start a new session", native.Target)}
		}
		if native := msg.NativeTools; native != nil {
			if native.OutcomeUnknown {
				return nil, &NativeToolError{Cause: fmt.Errorf("native request outcome is unknown; inspect the saved receipt and start a new session")}
			}
			for _, call := range native.Calls {
				pending[call.ID] = len(call.Result) == 0 && call.Error == ""
			}
			pendingAuthorization = native.Authorization
		}
	}
	hasPending := false
	for _, unresolved := range pending {
		hasPending = hasPending || unresolved
	}
	policy, _ := ctx.Value(nativePolicyContextKey{}).(*NativeToolPolicy)
	request, err := resolveNativeToolRequest(provider, model, tuning, policy)
	if err != nil {
		return nil, &NativeToolError{Cause: err}
	}
	if request == nil {
		if hasPending {
			return nil, &NativeToolError{Cause: fmt.Errorf("pending native tool authorization is no longer available")}
		}
		return impl.CompleteStream(ctx, key, model, system, messages, defs, maxTokens, tuning, cb)
	}
	if policy.Begin == nil || policy.Finish == nil {
		return nil, &NativeToolError{Cause: fmt.Errorf("durable native request journal is unavailable")}
	}
	if provider.IsCodexOAuthTransport() || !requestOverridesEmpty(provider.RequestOverrides(model)) {
		return nil, &NativeToolError{Cause: fmt.Errorf("native tools require an explicit API endpoint without request overrides")}
	}
	authorization := request.authorization
	if err := validateNativeToolBinding(provider, model, authorization, defs); err != nil {
		return nil, &NativeToolError{Cause: err}
	}
	if hasPending && !authorization.Equal(pendingAuthorization) {
		return nil, &NativeToolError{Cause: fmt.Errorf("pending native tool authorization changed")}
	}
	tuning.nativeTool = request
	defs = slices.DeleteFunc(slices.Clone(defs), func(def message.ToolDefinition) bool { return def.Name == authorization.Tool })
	history := &message.NativeToolHistory{Authorization: authorization, Target: target, Protocol: provider.Type(), APIURL: provider.APIURL()}
	defer func() {
		if failure, ok := errors.AsType[*NativeToolError](retErr); ok && len(history.RequestIDs) > 0 {
			history.OutcomeUnknown = true
			failure.Receipt = history
			if policy.Failed != nil {
				policy.Failed(history)
			}
		}
	}()
	var text strings.Builder
	for continuation := 0; continuation <= 4; continuation++ {
		if err := ctx.Err(); err != nil {
			return nil, &NativeToolError{Cause: err}
		}
		if !policy.Permitted(authorization.Tool) {
			return nil, &NativeToolError{Cause: fmt.Errorf("native request authorization was revoked")}
		}
		id, err := policy.Begin(ctx, NativeRequestRecord{Tuning: tuning, ServiceTier: serviceTier, Target: target, Protocol: provider.Type(), APIURL: provider.APIURL(), Authorization: authorization, System: system, Messages: messages, Tools: defs, MaxTokens: maxTokens, Continuation: continuation})
		if err != nil {
			return nil, &NativeToolError{Cause: fmt.Errorf("persist native request authorization: %w", err)}
		}
		history.RequestIDs = append(history.RequestIDs, id)
		started := time.Now()
		requestCtx, dispatch := WithRequestDispatch(ctx, nil)
		resp, callErr := impl.CompleteStream(requestCtx, key, model, system, messages, defs, maxTokens, tuning, cb)
		if resp == nil {
			if callErr == nil {
				callErr = fmt.Errorf("provider returned no response")
			}
			resp = &message.Response{}
		}
		if callErr == nil && ctx.Err() != nil {
			callErr = ctx.Err()
		}
		if resp != nil {
			resp.NativeRequestDuration = time.Since(started)
			normalizeResponseUsage(provider, resp)
			if resp.Hosted != nil && resp.Hosted.RequiresApproval {
				callErr = fmt.Errorf("provider requested approval; no approval was sent")
			}
			if resp.StopReason == "interrupted" {
				callErr = fmt.Errorf("native response was interrupted")
			}
			if resp.StopReason == "max_tokens" || resp.StopReason == "length" {
				callErr = fmt.Errorf("native response reached its output limit; automatic replay is disabled")
			}
			for _, call := range resp.ToolCalls {
				if call.ID == "" || call.Name == "" || !json.Valid(call.Args) {
					callErr = fmt.Errorf("native response contains malformed client tool calls")
				}
			}
			if resp.StopReason == "pause_turn" && (continuation == 4 || resp.Hosted == nil || len(resp.Hosted.Items) == 0 || len(resp.ToolCalls) > 0) {
				callErr = fmt.Errorf("native pause cannot be continued safely")
			}
			if resp.Hosted != nil {
				if resp.Hosted.Container != "" {
					history.Container = resp.Hosted.Container
				}
				history.Items = append(history.Items, resp.Hosted.Items...)
				history.Calls = message.MergeHostedCalls(history.Calls, resp.Hosted.Calls)
				for _, call := range resp.Hosted.Calls {
					pending[call.ID] = len(call.Result) == 0 && call.Error == ""
				}
			}
			if callErr == nil && resp.StopReason != "pause_turn" && len(resp.ToolCalls) == 0 {
				for _, unresolved := range pending {
					if unresolved {
						callErr = fmt.Errorf("native tool outcome is unknown because the result is missing")
						break
					}
				}
			}
			text.WriteString(resp.Content)
			resp.NativeTools = history
		}
		outcome := nativeRequestOutcome(dispatch, callErr)
		if outcome.Unexecuted() {
			resp.NativeTools = nil
		}
		if err := policy.Finish(id, outcome, resp, callErr); err != nil {
			return nil, &NativeToolError{Cause: fmt.Errorf("native result persistence failed; outcome must be reconciled: %w", err)}
		}
		if outcome.Unexecuted() {
			history.RequestIDs = history.RequestIDs[:len(history.RequestIDs)-1]
			if len(history.RequestIDs) == 0 && !hasPending {
				if outcome == message.NativeRequestNotSent {
					// A local request-construction failure needs correction, not
					// another key. Its durable not_sent result releases the session.
					return nil, &NativeToolError{Cause: callErr}
				}
				return nil, callErr
			}
		}
		if callErr != nil {
			return nil, &NativeToolError{Cause: callErr}
		}
		if resp.StopReason != "pause_turn" {
			resp.Content = text.String()
			if request.formatContent != nil {
				resp.Content = request.formatContent(history, text.String())
			}
			return resp, nil
		}
		receipt := *history
		receipt.Items = slices.Clone(resp.Hosted.Items)
		messages = append(slices.Clone(messages), message.Message{Role: message.RoleAssistant, Content: resp.Content, NativeTools: &receipt})
	}
	panic("unreachable native continuation")
}

// Forced client-tool choices retain the ordinary tool surface and execution
// path. A native tool cannot satisfy a caller's client-tool requirement.
func nativeToolChoiceAllowed(protocol string, tuning RequestTuning) bool {
	choice := tuning.OpenAI.ToolChoice
	if protocol == config.ProviderTypeMessages {
		choice = tuning.Anthropic.ToolChoice
	}
	return choice == "" || choice == "auto"
}

func (item responsesInputItem) MarshalJSON() ([]byte, error) {
	if len(item.Raw) > 0 {
		return item.Raw, nil
	}
	type wire responsesInputItem
	return json.Marshal(wire(item))
}
func (item anthropicContent) MarshalJSON() ([]byte, error) {
	if len(item.Raw) > 0 {
		return marshalAnthropicNativeContent(item)
	}
	type wire anthropicContent
	return json.Marshal(wire(item))
}
