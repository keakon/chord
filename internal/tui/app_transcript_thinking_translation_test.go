package tui

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestMessagesToBlocksRestoresThinkingTranslationsByHash(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "thinking"}}, Content: "done"},
	}
	translations := map[string]ThinkingTranslationView{
		thinkingTranslationTranscriptKey("msgidx:1", 0, recovery.ThinkingTranslationOriginalHash("thinking")): {
			TargetLang: "zh-Hans",
			Content:    "思考",
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
		thinkingTranslationTranscriptKey("msgidx:0", 0, recovery.ThinkingTranslationOriginalHash("old thinking")): {
			TargetLang: "zh-Hans",
			Content:    "旧翻译",
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
