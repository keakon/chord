package thinkingtranslate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type stubChunkTranslator struct {
	translate func(ctx context.Context, targetLang, chunk string) (string, error)
}

func (s stubChunkTranslator) TranslateChunk(ctx context.Context, targetLang, chunk string) (string, error) {
	return s.translate(ctx, targetLang, chunk)
}

type sequenceProvider struct {
	mu    sync.Mutex
	calls int
	steps []string
}

func (p *sequenceProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx := p.calls
	p.calls++
	if idx >= len(p.steps) {
		return nil, errors.New("unexpected provider call")
	}
	step := p.steps[idx]
	if step == "empty" {
		return &message.Response{Content: ""}, nil
	}
	return &message.Response{Content: step}, nil
}

func newTranslatorClient(t *testing.T, provider llm.Provider) *llm.Client {
	t.Helper()
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"first":  {Limit: config.ModelLimit{Context: 4096, Output: 2048}},
			"second": {Limit: config.ModelLimit{Context: 4096, Output: 2048}},
		},
	}, nil)
	client := llm.NewClient(providerCfg, provider, "first", 2048, "")
	client.SetModelPool([]llm.FallbackModel{
		{ProviderConfig: providerCfg, ProviderImpl: provider, ModelID: "first", MaxTokens: 2048},
		{ProviderConfig: providerCfg, ProviderImpl: provider, ModelID: "second", MaxTokens: 2048},
	}, 0)
	return client
}

func TestLLMTranslatorFallsBackOnEmptyTranslation(t *testing.T) {
	provider := &sequenceProvider{steps: []string{
		"empty",
		"<TRANSLATION>第二个模型成功</TRANSLATION>",
	}}
	translator := &LLMTranslator{NewClient: func() (*llm.Client, error) {
		return newTranslatorClient(t, provider), nil
	}}

	got, err := translator.TranslateChunk(context.Background(), "zh-Hans", "Hello")
	if err != nil {
		t.Fatalf("TranslateChunk() error = %v, want nil", err)
	}
	if got != "第二个模型成功" {
		t.Fatalf("TranslateChunk() = %q, want %q", got, "第二个模型成功")
	}
	if provider.calls != 2 {
		t.Fatalf("provider.calls = %d, want 2", provider.calls)
	}
}

func TestTranslationPromptUsesConsistentStructuredTags(t *testing.T) {
	msgs := translationPrompt("zh-Hans", "Hello")
	if len(msgs) != 1 {
		t.Fatalf("translationPrompt() messages = %d, want 1", len(msgs))
	}
	prompt := msgs[0].Content
	for _, want := range []string{
		"content inside <TEXT>",
		"<TEXT> and </TEXT> as delimiters",
		"enclosed in <TRANSLATION></TRANSLATION>",
		"<TEXT>\nHello\n</TEXT>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %q", want, prompt)
		}
	}
	for _, forbidden := range []string{"source_text", "SOURCE_TEXT", "<OUTPUT>", "</OUTPUT>"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains inconsistent tag %q: %q", forbidden, prompt)
		}
	}
}

func TestServiceTranslateTextUsesConfiguredTranslator(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.TargetLang = "zh-Hans"
	svc.SetTranslator(stubChunkTranslator{translate: func(ctx context.Context, targetLang, chunk string) (string, error) {
		if targetLang != "zh-Hans" {
			t.Fatalf("targetLang = %q, want zh-Hans", targetLang)
		}
		if chunk != "Hello, world" {
			t.Fatalf("chunk = %q, want Hello, world", chunk)
		}
		return "你好，世界", nil
	}})
	got, err := svc.TranslateText(context.Background(), "Hello, world", nil)
	if err != nil {
		t.Fatalf("TranslateText() error: %v", err)
	}
	if got != "你好，世界" {
		t.Fatalf("TranslateText() = %q, want %q", got, "你好，世界")
	}
}

func TestServiceFailureDoesNotBlockLaterTranslations(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	calls := 0
	boom := errors.New("temporary failure")
	svc.SetTranslator(stubChunkTranslator{translate: func(ctx context.Context, targetLang, chunk string) (string, error) {
		calls++
		if calls == 1 {
			return "", boom
		}
		return "第二次成功", nil
	}})

	_, err = svc.TranslateText(context.Background(), "First", nil)
	if !errors.Is(err, boom) {
		t.Fatalf("first TranslateText() error = %v, want %v", err, boom)
	}
	got, err := svc.TranslateText(context.Background(), "Second", nil)
	if err != nil {
		t.Fatalf("second TranslateText() error: %v", err)
	}
	if got != "第二次成功" {
		t.Fatalf("second TranslateText() = %q, want %q", got, "第二次成功")
	}
	if calls != 2 {
		t.Fatalf("translator calls = %d, want 2", calls)
	}
}
