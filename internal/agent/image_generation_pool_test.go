package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

type poolTestBackend struct {
	target imagegen.Target
	fail   error
	runs   int
}

func (b *poolTestBackend) Target() imagegen.Target                           { return b.target }
func (*poolTestBackend) Timeout() time.Duration                              { return time.Minute }
func (*poolTestBackend) Check(ctx context.Context, _ imagegen.Request) error { return ctx.Err() }
func (*poolTestBackend) Download(context.Context, string) (imagegen.Image, error) {
	return imagegen.Image{}, nil
}
func (b *poolTestBackend) Run(_ context.Context, _ imagegen.Request, before func() error) (*imagegen.Result, error) {
	b.runs++
	if err := before(); err != nil {
		return nil, err
	}
	if b.fail != nil {
		return nil, b.fail
	}
	return &imagegen.Result{}, nil
}

func TestImagePoolCapabilityCursorAndUnknownBarrier(t *testing.T) {
	a := &MainAgent{globalConfig: config.DefaultConfig()}
	openai, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", "")
	gemini, _ := imagegen.ResolveTarget(imagegen.PresetGemini, "gemini-3.1-flash-image-preview", "")
	first := &poolTestBackend{target: openai, fail: &imagegen.Failure{State: imagegen.StateRejected, RetryKey: true, Cause: fmt.Errorf("quota rejected")}}
	second := &poolTestBackend{target: gemini}
	pool := NewImageGenerationPool(a, []tools.ImageGenerationBackend{first, second}).(*imageGenerationPool)
	r := imagegen.Request{Prompt: "A tree", Operation: imagegen.Generate}
	runner, err := pool.Prepare(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	if _, err = runner.Run(t.Context(), r, func() error { targets = append(targets, runner.Target().Preset); return nil }); err != nil {
		t.Fatal(err)
	}
	if first.runs != 1 || second.runs != 1 || len(targets) != 2 || targets[1] != imagegen.PresetGemini {
		t.Fatal("pool did not traverse all eligible targets")
	}
	next, err := pool.Prepare(t.Context(), r)
	if err != nil || next.Target().Preset != imagegen.PresetGemini {
		t.Fatal("successful cursor lost")
	}
	r.Background = "transparent"
	next, err = pool.Prepare(t.Context(), r)
	if err != nil || next.Target().Preset != imagegen.PresetOpenAI {
		t.Fatal("unsupported transparency silently dropped")
	}
	first.fail = &imagegen.Failure{State: imagegen.StateUnknown, Cause: fmt.Errorf("connection lost")}
	if _, err = next.Run(t.Context(), r, func() error { return nil }); err == nil || second.runs != 1 {
		t.Fatal("unknown paid request replayed on another target")
	}
	original, err := pool.Restore(t.Context(), tools.ImageTargetFingerprint(openai))
	if err != nil || original != first {
		t.Fatal("restore used cursor instead of original target")
	}
	if _, err = pool.Restore(t.Context(), "missing"); err == nil {
		t.Fatal("changed target accepted")
	}
	r.Quality = "invalid"
	if _, err = pool.Prepare(t.Context(), r); err == nil {
		t.Fatal("unsupported operation accepted")
	}
}

func TestImagePoolFallbackDeadline(t *testing.T) {
	turn := &Turn{ID: 1}
	turn.nativeImageFallback.Store(true)
	turn.nativeImageDeadline.Store(time.Now().Add(-time.Second).UnixNano())
	a := &MainAgent{globalConfig: config.DefaultConfig(), turn: turn}
	target, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", "")
	pool := NewImageGenerationPool(a, []tools.ImageGenerationBackend{&poolTestBackend{target: target}}).(*imageGenerationPool)
	ctx := tools.WithTurnID(t.Context(), 1)
	if _, err := pool.Prepare(ctx, imagegen.Request{Prompt: "A tree"}); err == nil {
		t.Fatal("expired fallback restarted a fresh time budget")
	}
}

func TestImagePoolOptionalParametersAndFailureDiagnostics(t *testing.T) {
	a := &MainAgent{globalConfig: config.DefaultConfig()}
	openai, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", "")
	openai.Provider = "openai"
	gemini, _ := imagegen.ResolveTarget(imagegen.PresetGemini, "gemini-3.1-flash-image-preview", "")
	gemini.Provider = "gemini"
	first := &poolTestBackend{target: openai}
	rejected := &imagegen.Failure{State: imagegen.StateRejected, RetryKey: true, Details: imagegen.FailureDetails{Category: imagegen.FailureQuota}, Cause: fmt.Errorf("quota rejected")}
	second := &poolTestBackend{target: gemini, fail: rejected}
	pool := NewImageGenerationPool(a, []tools.ImageGenerationBackend{first, second}).(*imageGenerationPool)
	request := imagegen.Request{Prompt: "A tree", Operation: imagegen.Generate}
	runner, err := pool.Prepare(t.Context(), request)
	if err != nil || runner.Target().Preset != imagegen.PresetOpenAI {
		t.Fatalf("default target=%v err=%v", runner, err)
	}
	request.Size = "1K"
	runner, err = pool.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request, func() error { return nil })
	failure, ok := errors.AsType[*imagegen.Failure](err)
	if !ok || first.runs != 0 || second.runs != 1 || len(failure.Details.SkippedTargets) != 1 || !strings.Contains(failure.Details.SkippedTargets[0], "openai/gpt-image-1.5") {
		t.Fatalf("failure=%+v", failure)
	}
	if len(rejected.Details.SkippedTargets) != 0 {
		t.Fatal("shared failure mutated")
	}
}

func TestImagePoolSkipsExhaustedCredentials(t *testing.T) {
	a := &MainAgent{globalConfig: config.DefaultConfig()}
	target, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", "")
	first := &poolTestBackend{target: target, fail: &imagegen.Failure{State: imagegen.StateNotSent, Cause: &llm.NoUsableKeysError{}}}
	second := &poolTestBackend{target: target}
	pool := NewImageGenerationPool(a, []tools.ImageGenerationBackend{first, second}).(*imageGenerationPool)
	request := imagegen.Request{Prompt: "A tree", Operation: imagegen.Generate}
	runner, err := pool.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(t.Context(), request, func() error { return nil }); err != nil || second.runs != 1 {
		t.Fatalf("fallback err=%v runs=%d", err, second.runs)
	}
}
