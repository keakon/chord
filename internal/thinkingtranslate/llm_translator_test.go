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
		"<TRANSLATION>备用模型返回了有效翻译。</TRANSLATION>",
	}}
	translator := &LLMTranslator{NewClient: func() (*llm.Client, error) {
		return newTranslatorClient(t, provider), nil
	}}

	got, err := translator.TranslateChunk(context.Background(), "zh-Hans", "The sample text should be translated by the fallback model.")
	if err != nil {
		t.Fatalf("TranslateChunk() error = %v, want nil", err)
	}
	if got != "备用模型返回了有效翻译。" {
		t.Fatalf("TranslateChunk() = %q, want %q", got, "备用模型返回了有效翻译。")
	}
	if provider.calls != 2 {
		t.Fatalf("provider.calls = %d, want 2", provider.calls)
	}
}

func TestLLMTranslatorFallsBackOnClearlyInvalidShortTranslation(t *testing.T) {
	provider := &sequenceProvider{steps: []string{
		"<TRANSLATION>**</TRANSLATION>",
		"<TRANSLATION>翻译结果应保留原文的主要含义，并为读者提供足够清晰的说明。</TRANSLATION>",
	}}
	translator := &LLMTranslator{NewClient: func() (*llm.Client, error) {
		return newTranslatorClient(t, provider), nil
	}}
	original := "The translation result should preserve the main idea of the source text and provide enough detail for a reader to compare the fallback output with the original content."

	got, err := translator.TranslateChunk(context.Background(), "zh-Hans", original)
	if err != nil {
		t.Fatalf("TranslateChunk() error = %v, want nil", err)
	}
	if got != "翻译结果应保留原文的主要含义，并为读者提供足够清晰的说明。" {
		t.Fatalf("TranslateChunk() = %q, want %q", got, "翻译结果应保留原文的主要含义，并为读者提供足够清晰的说明。")
	}
	if provider.calls != 2 {
		t.Fatalf("provider.calls = %d, want 2", provider.calls)
	}
}

func TestLLMTranslatorFallsBackOnWrongTargetLanguage(t *testing.T) {
	provider := &sequenceProvider{steps: []string{
		"<TRANSLATION>The translation result is still written in English, so it does not match the requested target language.</TRANSLATION>",
		"<TRANSLATION>翻译结果现在使用请求的目标语言，并保留了原文的主要含义。</TRANSLATION>",
	}}
	translator := &LLMTranslator{NewClient: func() (*llm.Client, error) {
		return newTranslatorClient(t, provider), nil
	}}
	original := "The translation result should preserve the main idea of the source text and provide enough detail for a reader to compare the fallback output with the original content."

	got, err := translator.TranslateChunk(context.Background(), "zh-Hans", original)
	if err != nil {
		t.Fatalf("TranslateChunk() error = %v, want nil", err)
	}
	if got != "翻译结果现在使用请求的目标语言，并保留了原文的主要含义。" {
		t.Fatalf("TranslateChunk() = %q, want %q", got, "翻译结果现在使用请求的目标语言，并保留了原文的主要含义。")
	}
	if provider.calls != 2 {
		t.Fatalf("provider.calls = %d, want 2", provider.calls)
	}
}

func TestClearlyInvalidTranslationAllowsShortOriginals(t *testing.T) {
	if IsClearlyInvalidTranslation("OK", "zh-Hans", "好") {
		t.Fatal("IsClearlyInvalidTranslation() rejected a short valid translation")
	}
}

func TestClearlyInvalidTranslationRejectsShortSymbolOnlyTranslation(t *testing.T) {
	if !IsClearlyInvalidTranslation("Short heading", "zh-Hans", "**") {
		t.Fatal("IsClearlyInvalidTranslation() accepted a symbol-only short translation")
	}
}

func TestClearlyInvalidTranslationRejectsSeverelyCompressedLongOriginal(t *testing.T) {
	original := "The sample explanation describes a translation fallback scenario with enough detail to verify that a very short output has lost important meaning from the source text. The validation should reject the output before it is accepted as a useful translation."
	if !IsClearlyInvalidTranslation(original, "zh-Hans", "过短的摘要") {
		t.Fatal("IsClearlyInvalidTranslation() accepted a severely compressed long translation")
	}
}

func TestClearlyInvalidTranslationAllowsReasonableCompression(t *testing.T) {
	original := "The sample explanation describes a translation fallback scenario with enough detail to verify that the translated output still preserves the important meaning from the source text."
	translated := "示例说明描述了翻译备用流程，并保留了原文中的重要含义。"
	if IsClearlyInvalidTranslation(original, "zh-Hans", translated) {
		t.Fatal("IsClearlyInvalidTranslation() rejected a reasonably compressed translation")
	}
}

func TestClearlyInvalidTranslationRejectsExcessiveWordCountRatio(t *testing.T) {
	original := "The translator should preserve enough words from the original reasoning to keep the meaning clear for readers."
	translated := "译文过短"
	if !IsClearlyInvalidTranslation(original, "zh-Hans", translated) {
		t.Fatal("IsClearlyInvalidTranslation() accepted an excessive word-count ratio")
	}
}

func TestClearlyInvalidTranslationAllowsReasonableWordCountRatio(t *testing.T) {
	original := "The translator should preserve enough words from the original reasoning to keep the meaning clear for readers."
	translated := "译文应保留原始推理中的足够信息，让读者能清楚理解其含义。"
	if IsClearlyInvalidTranslation(original, "zh-Hans", translated) {
		t.Fatal("IsClearlyInvalidTranslation() rejected a reasonable word-count ratio")
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

func TestServiceTranslateTextTranslatesOnlyPreview(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.MaxChars = 5
	calls := 0
	svc.SetTranslator(stubChunkTranslator{translate: func(ctx context.Context, targetLang, chunk string) (string, error) {
		calls++
		if chunk != "abcde" {
			t.Fatalf("chunk = %q, want abcde", chunk)
		}
		return "预览", nil
	}})
	got, err := svc.TranslateText(context.Background(), "abcdefghij", nil)
	if err != nil {
		t.Fatalf("TranslateText() error: %v", err)
	}
	if got != "预览" {
		t.Fatalf("TranslateText() = %q, want 预览", got)
	}
	if calls != 1 {
		t.Fatalf("translator calls = %d, want 1", calls)
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
