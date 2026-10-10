package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type imageGenerationBackend struct {
	agent        *MainAgent
	target       imagegen.Target
	timeout      time.Duration
	provider     *llm.ProviderConfig
	client       *http.Client
	providerName string
}

// NewImageGenerationBackend keeps the image credential health separate from
// chat health while reusing the provider's key selection and proxy semantics.
func NewImageGenerationBackend(a *MainAgent, target imagegen.Target, timeout time.Duration, provider config.ProviderConfig, keys []string, globalProxy string) (tools.ImageGenerationBackend, error) {
	target.UserAgent = provider.UserAgent
	if len(keys) == 0 {
		return nil, fmt.Errorf("image_generation provider %q has no API key", target.Provider)
	}
	if provider.Preset == config.ProviderPresetCodex || provider.TokenURL != "" {
		return nil, fmt.Errorf("image_generation requires API-key credentials; OAuth image backends are not enabled")
	}
	headerTimeout := timeout
	if provider.ResponseHeaderTimeout > 0 {
		headerTimeout = min(timeout, time.Duration(provider.ResponseHeaderTimeout)*time.Second)
	}
	client, err := llm.NewHTTPClientWithProxyAndHeaderTimeout(llm.ResolveEffectiveProxy(provider.Proxy, globalProxy), timeout, headerTimeout)
	if err != nil {
		return nil, fmt.Errorf("create image HTTP transport: %w", err)
	}
	keyPool := llm.NewProviderConfig(target.Provider, provider, keys)
	keyPool.SetRateLimiter(provider.RateLimit)
	return &imageGenerationBackend{agent: a, target: target, timeout: timeout, provider: keyPool, client: client, providerName: target.Provider}, nil
}

func (b *imageGenerationBackend) Target() imagegen.Target { return b.target }
func (b *imageGenerationBackend) Timeout() time.Duration  { return b.timeout }

func (b *imageGenerationBackend) caller(ctx context.Context) (permission.Ruleset, permission.PathScope, error) {
	id := tools.AgentIDFromContext(ctx)
	if id != "" && id != identity.MainAgentID && id != b.agent.instanceID {
		sub := b.agent.subAgentByID(id)
		if sub == nil {
			return nil, permission.PathScope{}, fmt.Errorf("image generation caller is no longer active")
		}
		return sub.currentRuleset(), sub.effectivePathScope(), nil
	}
	return b.agent.effectiveRuleset(), b.agent.effectivePathScope(), nil
}

func (b *imageGenerationBackend) Check(ctx context.Context, r imagegen.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rules, scope, err := b.caller(ctx)
	if err != nil {
		return err
	}
	guard, guarded := tools.ImageAccessGuardFromContext(ctx)
	check := func(tool, path string) error {
		if guarded {
			return guard(tool, path)
		}
		action := rules.EvaluatePath(tool, path, scope)
		if tool == tools.NameGenerateImage {
			action = rules.Evaluate(tool, path)
		}
		if len(rules) == 0 || action == permission.ActionAllow {
			return nil
		}
		if action == permission.ActionAsk {
			return wrapToolRequiresConfirmation(tools.NameGenerateImage)
		}
		return wrapToolPermissionDenied(tools.NameGenerateImage)
	}
	for _, resource := range imageGenerationResources(r) {
		path := resource.path
		if !guarded && resource.tool != tools.NameGenerateImage {
			path, err = tools.ResolveImageArtifactPath(tools.SessionDirFromContext(ctx), path, scope.Cwd)
			if err != nil {
				return err
			}
		}
		if err := check(resource.tool, path); err != nil {
			return err
		}
	}
	return nil
}

func (b *imageGenerationBackend) Run(ctx context.Context, r imagegen.Request, beforeSend func() error) (result *imagegen.Result, err error) {
	defer func() {
		if failure, ok := errors.AsType[*imagegen.Failure](err); ok {
			copy := *failure
			copy.Provider, copy.Model = b.target.Provider, b.target.Model
			err = &copy
		}
	}()
	attempts := b.provider.KeyCount()
	seen := make(map[string]bool, attempts)
	var last error
	for range attempts {
		if err := b.Check(ctx, r); err != nil {
			return nil, &imagegen.Failure{State: imagegen.StateNotSent, Cause: err}
		}
		key, _, err := b.provider.SelectKeyWithContext(ctx)
		if err != nil {
			if last != nil {
				_, cooling := errors.AsType[*llm.AllKeysCoolingError](err)
				_, exhausted := errors.AsType[*llm.NoUsableKeysError](err)
				if cooling || exhausted {
					return nil, last
				}
			}
			failure := &imagegen.Failure{State: imagegen.StateNotSent, Cause: err}
			if cooling, ok := errors.AsType[*llm.AllKeysCoolingError](err); ok {
				failure.Details = imagegen.FailureDetails{Category: imagegen.FailureRateLimit, RetryAfterSeconds: new(max(cooling.RetryAfter.Seconds(), 0))}
			} else if _, ok := errors.AsType[*llm.NoUsableKeysError](err); ok {
				failure.Cause = fmt.Errorf("no usable image API credentials; update credentials or billing before retrying: %w", err)
			}
			return nil, failure
		}
		if seen[key] {
			break
		}
		seen[key] = true
		release, err := b.agent.governor.acquireLLM(ctx, b.providerName+"/"+b.target.Model)
		if err != nil {
			return nil, &imagegen.Failure{State: imagegen.StateNotSent, Cause: err}
		}
		attemptStarted := time.Now()
		result, err := imagegen.Execute(ctx, b.client, b.target, r, key, func() error {
			if err := b.Check(ctx, r); err != nil {
				return err
			}
			return beforeSend()
		})
		release()
		if err == nil {
			b.provider.MarkKeySuccess(key)
			return result, nil
		}
		logImageRequestFailure(ctx, b.target, time.Since(attemptStarted), err)
		last = err
		failure, ok := errors.AsType[*imagegen.Failure](err)
		if !ok || !failure.RetryKey {
			return nil, err
		}
		// This is the private image key pool; rejection never disables chat keys.
		switch failure.Details.Category {
		case imagegen.FailureAuthentication, imagegen.FailureQuota:
			b.provider.MarkDeactivated(key)
		default:
			delay := llm.RetryAfterForProvider(b.provider, &llm.APIError{RetryAfter: failure.Details.RetryAfter()})
			b.provider.MarkRateLimitCooldown(key, delay)
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, &imagegen.Failure{State: imagegen.StateNotSent, Cause: fmt.Errorf("no eligible image API keys")}
}

func (b *imageGenerationBackend) Download(ctx context.Context, url string) (imagegen.Image, error) {
	return imagegen.Download(ctx, b.client, url, func(raw string) error {
		rules, _, err := b.caller(ctx)
		if err != nil {
			return err
		}
		// Downloads are internal to an authorized generation. Explicit network
		// restrictions still apply; an ask requires a separate user-approved fetch.
		if match := rules.EvaluateWebFetch(raw); match.Found && match.Rule.Action != permission.ActionAllow {
			return fmt.Errorf("image result download blocked by web_fetch network permission")
		}
		return nil
	})
}
