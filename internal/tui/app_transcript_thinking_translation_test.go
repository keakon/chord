package tui

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestMessagesToBlocksRestoresThinkingTranslationsByMessageBlock(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "thinking"}}, Content: "done"},
	}
	translations := map[string]ThinkingTranslationView{
		thinkingTranslationTranscriptKey("msgidx:1", 0): {
			TargetLang:   "zh-Hans",
			Content:      "思考",
			OriginalHash: recovery.ThinkingTranslationOriginalHash("thinking"),
		},
	}
	var nextID int
	blocks := messagesToBlocksWithThinkingTranslations(msgs, &nextID, translations)
	if len(blocks) < 2 {
		t.Fatalf("blocks len = %d, want at least 2", len(blocks))
	}
	thinking := blocks[1]
	if thinking.Type != BlockThinking {
		t.Fatalf("blocks[1].Type = %v, want BlockThinking", thinking.Type)
	}
	if len(thinking.ThinkingTranslations) != 1 {
		t.Fatalf("ThinkingTranslations len = %d, want 1", len(thinking.ThinkingTranslations))
	}
	if got := thinking.ThinkingTranslations[0].Content; got != "思考" {
		t.Fatalf("translation = %q, want 思考", got)
	}
}

func TestMessagesToBlocksSkipsStaleThinkingTranslationHash(t *testing.T) {
	msgs := []message.Message{{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "new thinking"}}}}
	translations := map[string]ThinkingTranslationView{
		thinkingTranslationTranscriptKey("msgidx:0", 0): {
			TargetLang:   "zh-Hans",
			Content:      "旧翻译",
			OriginalHash: recovery.ThinkingTranslationOriginalHash("old thinking"),
		},
	}
	var nextID int
	blocks := messagesToBlocksWithThinkingTranslations(msgs, &nextID, translations)
	if len(blocks) != 1 || blocks[0].Type != BlockThinking {
		t.Fatalf("blocks = %#v, want one thinking block", blocks)
	}
	if len(blocks[0].ThinkingTranslations) != 0 {
		t.Fatalf("stale translation should not be restored, got %#v", blocks[0].ThinkingTranslations)
	}
}

func TestMessagesToBlocksRestoresReasoningContentAsThinkingFallback(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "I should greet the user.",
		Content:          "Hi!",
	}}
	var nextID int
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	if blocks[0].Type != BlockThinking || blocks[0].Content != "I should greet the user." {
		t.Fatalf("first block = %#v, want restored reasoning content as thinking", blocks[0])
	}
	if blocks[1].Type != BlockAssistant || blocks[1].Content != "Hi!" {
		t.Fatalf("second block = %#v, want assistant content", blocks[1])
	}
}

func TestMessagesToBlocksPrefersThinkingBlocksOverReasoningContent(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ThinkingBlocks:   []message.ThinkingBlock{{Thinking: "structured thinking"}},
		ReasoningContent: "fallback thinking",
	}}
	var nextID int
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("blocks len = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockThinking || blocks[0].Content != "structured thinking" {
		t.Fatalf("block = %#v, want structured thinking block", blocks[0])
	}
}

func TestMessagesToBlocksReusesTranslationAfterTargetLangChange(t *testing.T) {
	msgs := []message.Message{{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "thinking"}}}}
	translations := map[string]ThinkingTranslationView{
		thinkingTranslationTranscriptKey("msgidx:0", 0): {
			TargetLang:   "ja",
			Content:      "考え",
			OriginalHash: recovery.ThinkingTranslationOriginalHash("thinking"),
		},
	}
	var nextID int
	blocks := messagesToBlocksWithThinkingTranslations(msgs, &nextID, translations)
	if len(blocks) != 1 || blocks[0].Type != BlockThinking {
		t.Fatalf("blocks = %#v, want one thinking block", blocks)
	}
	if len(blocks[0].ThinkingTranslations) != 1 || blocks[0].ThinkingTranslations[0].Content != "考え" {
		t.Fatalf("expected restored translation regardless of target_lang, got %#v", blocks[0].ThinkingTranslations)
	}
}
