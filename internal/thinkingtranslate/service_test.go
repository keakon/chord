package thinkingtranslate

import (
	"testing"
)

func TestNormalizeLangCode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Chinese variants
		{"zh", "zh"},
		{"zh-Hans", "zh"},
		{"zh-hans", "zh"},
		{"zh-CN", "zh"},
		{"zh-cn", "zh"},
		{"zh-Hant", "zh"},
		{"zh-hant", "zh"},
		{"zh-TW", "zh"},
		{"zh-HK", "zh"},

		// Other languages with regions
		{"en", "en"},
		{"en-US", "en"},
		{"en-GB", "en"},
		{"de-DE", "de"},
		{"fr-FR", "fr"},
		{"ja", "ja"},

		// Edge cases
		{"", ""},
		{"  zh-Hans  ", "zh"},
		{"EN-us", "en"},
	}

	for _, tt := range tests {
		got := normalizeLangCode(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeLangCode(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestShouldTranslate_LanguageCodeNormalization(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.TargetLang = "zh-Hans"
	svc.MinConfidence = 0.7
	svc.LatinRatioTrigger = 0.7

	t.Run("zh detected should match zh-Hans target", func(t *testing.T) {
		// This is the bug case: whatlanggo returns "zh", but target is "zh-Hans"
		svc.DetectLang = func(string) (string, float64) { return "zh", 1.0 }
		trigger, meta := svc.ShouldTranslate("zh-Hans", "这是中文内容")
		if trigger {
			t.Errorf("Should not trigger for zh vs zh-Hans, but got reason=%q", meta.Reason)
		}
	})

	t.Run("en detected should trigger for zh-Hans user", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		trigger, meta := svc.ShouldTranslate("zh-Hans", "This is English content")
		if !trigger || meta.Reason != "lang_mismatch" {
			t.Errorf("Should trigger for en vs zh-Hans, got trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("en-US detected should trigger for zh user", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en-US", 0.95 }
		trigger, meta := svc.ShouldTranslate("zh", "This is English content")
		if !trigger || meta.Reason != "lang_mismatch" {
			t.Errorf("Should trigger for en-US vs zh, got trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("zh-CN detected should match zh-Hans target", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "zh-CN", 0.99 }
		trigger, meta := svc.ShouldTranslate("zh-Hans", "这是简体中文")
		if trigger {
			t.Errorf("Should not trigger for zh-CN vs zh-Hans, but got reason=%q", meta.Reason)
		}
	})

	t.Run("zh-Hant detected should match zh-Hans target", func(t *testing.T) {
		// After normalization, both become "zh"
		svc.DetectLang = func(string) (string, float64) { return "zh-Hant", 0.95 }
		trigger, meta := svc.ShouldTranslate("zh-Hans", "這是繁體中文")
		if trigger && meta.Reason == "lang_mismatch" {
			t.Errorf("Should not trigger lang_mismatch for zh-Hant vs zh-Hans after normalization, got trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("mixed Chinese-English content should not trigger if Han >= 50%", func(t *testing.T) {
		// Simulate whatlanggo detecting as English but content is predominantly Chinese
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		// Constructed text: ~50 Chinese chars + ~20 English words = 50/(50+20) = 71% Han
		text := `这是一段包含技术术语的中文内容。我们讨论 refactor 和 optimization 的问题。
另外还有 performance 和 scalability 方面的考虑。整体来说中文内容占主导地位，
虽然包含一些 technical terms 但不应该触发翻译功能。因为主要语言仍然是中文。
我们继续分析这些技术问题的解决方案和最佳实践。`
		trigger, meta := svc.ShouldTranslate("zh-Hans", text)
		if trigger {
			t.Errorf("Should not trigger for mixed content with Han >= 50%%, got trigger=%v reason=%q", trigger, meta.Reason)
		}
		if meta.Reason != "target_is_dominant" {
			t.Errorf("Expected reason=target_is_dominant, got %q", meta.Reason)
		}
	})

	t.Run("pure English should trigger translation", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		trigger, meta := svc.ShouldTranslate("zh-Hans", "Let me continue to review the remaining commits and verify the implementation details")
		if !trigger || meta.Reason != "lang_mismatch" {
			t.Errorf("Should trigger for pure English, got trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("English-dominant with Chinese terms should trigger translation", func(t *testing.T) {
		// English text with a few Chinese terms: should still translate
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		// ~20 English words + ~4 Chinese chars = 20/(20+4) = 83% Latin
		text := `This document discusses some Chinese concepts like 功能 and 性能。
We need to understand the technical requirements 需求 and implementation 实现。`
		trigger, meta := svc.ShouldTranslate("zh-Hans", text)
		if !trigger || meta.Reason != "lang_mismatch" {
			t.Errorf("Should trigger for English-dominant content, got trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("mixed English-Chinese content for English user should not trigger if Latin >= 50%", func(t *testing.T) {
		// For English user receiving Chinese-mixed content
		svc.DetectLang = func(string) (string, float64) { return "zh", 0.95 }
		// Constructed: ~30 English words + ~10 Chinese chars = 30/(30+10) = 75% Latin
		text := `This is a document that discusses some Chinese concepts and terminology.
We need to understand the technical requirements and implementation details.
Some Chinese terms like 功能 性能 需求 实现 are mentioned but English is dominant.`
		trigger, meta := svc.ShouldTranslate("en", text)
		if trigger {
			t.Errorf("Should not trigger for mixed content with Latin >= 50%%, got trigger=%v reason=%q", trigger, meta.Reason)
		}
		if meta.Reason != "target_is_dominant" {
			t.Errorf("Expected reason=target_is_dominant, got %q", meta.Reason)
		}
	})

	t.Run("Japanese dominant content should not trigger if Kana/Han >= 50%", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		text := "これはレビュー結果の要約です。カタカナとひらがなを含む日本語が中心で、少量の English terms のみ含まれます。"
		trigger, meta := svc.ShouldTranslate("ja", text)
		if trigger {
			t.Errorf("Should not trigger for Japanese-dominant content, got trigger=%v reason=%q", trigger, meta.Reason)
		}
		if meta.Reason != "target_is_dominant" {
			t.Errorf("Expected reason=target_is_dominant, got %q", meta.Reason)
		}
	})

	t.Run("Korean dominant content should not trigger if Hangul >= 50%", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		text := "이 문서는 검토 결과를 설명합니다. 대부분이 한국어 문장이고 일부 technical terms 만 포함합니다."
		trigger, meta := svc.ShouldTranslate("ko", text)
		if trigger {
			t.Errorf("Should not trigger for Korean-dominant content, got trigger=%v reason=%q", trigger, meta.Reason)
		}
		if meta.Reason != "target_is_dominant" {
			t.Errorf("Expected reason=target_is_dominant, got %q", meta.Reason)
		}
	})
}
