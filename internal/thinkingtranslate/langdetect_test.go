package thinkingtranslate

import "testing"

func TestScriptRatios(t *testing.T) {
	latin, han := scriptRatios("Hello world")
	if latin < 0.9 || han != 0 {
		t.Fatalf("latin=%v han=%v", latin, han)
	}

	latin, han = scriptRatios("你好世界")
	if han < 0.9 || latin != 0 {
		t.Fatalf("latin=%v han=%v", latin, han)
	}

	latin, han = scriptRatios("hello 你好")
	if latin <= 0 || han <= 0 {
		t.Fatalf("latin=%v han=%v", latin, han)
	}
}

func TestScriptRatiosDetailed(t *testing.T) {
	latin, han, kana, hangul := scriptRatiosDetailed("こんにちは")
	if kana < 0.9 || latin != 0 || han != 0 || hangul != 0 {
		t.Fatalf("latin=%v han=%v kana=%v hangul=%v", latin, han, kana, hangul)
	}

	latin, han, kana, hangul = scriptRatiosDetailed("안녕하세요")
	if hangul < 0.9 || latin != 0 || han != 0 || kana != 0 {
		t.Fatalf("latin=%v han=%v kana=%v hangul=%v", latin, han, kana, hangul)
	}

	latin, han, kana, hangul = scriptRatiosDetailed("日本語のテキスト")
	if han <= 0 || kana <= 0 || latin != 0 || hangul != 0 {
		t.Fatalf("latin=%v han=%v kana=%v hangul=%v", latin, han, kana, hangul)
	}
}

func TestShouldTranslate(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.TargetLang = "zh-Hans"
	svc.MinConfidence = 0.7
	svc.LatinRatioTrigger = 0.7

	t.Run("lang mismatch triggers", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		trigger, meta := svc.ShouldTranslate("zh", "This is an English reasoning block.")
		if !trigger || meta.Reason != "lang_mismatch" {
			t.Fatalf("trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("high latin ratio fallback triggers", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "", 0 }
		trigger, meta := svc.ShouldTranslate("zh", "This is still mostly English text.")
		if !trigger || meta.Reason != "latin_ratio" {
			t.Fatalf("trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("same language does not trigger", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "zh", 0.99 }
		trigger, meta := svc.ShouldTranslate("zh", "这是中文思考")
		if trigger || meta.Reason != "no_trigger" {
			t.Fatalf("trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("low confidence mismatch falls back to no trigger for non zh", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "de", 0.2 }
		trigger, meta := svc.ShouldTranslate("en", "gemischt text")
		if trigger || meta.Reason != "no_trigger" {
			t.Fatalf("trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("Japanese dominant content suppresses mismatched translation trigger", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		trigger, meta := svc.ShouldTranslate("ja", "これは日本語のテキストです。カタカナとひらがなが中心です。")
		if trigger || meta.Reason != "target_is_dominant" {
			t.Fatalf("trigger=%v reason=%q", trigger, meta.Reason)
		}
	})

	t.Run("Korean dominant content suppresses mismatched translation trigger", func(t *testing.T) {
		svc.DetectLang = func(string) (string, float64) { return "en", 0.95 }
		trigger, meta := svc.ShouldTranslate("ko", "이 문장은 한국어 설명입니다. 한글 비중이 충분히 높습니다.")
		if trigger || meta.Reason != "target_is_dominant" {
			t.Fatalf("trigger=%v reason=%q", trigger, meta.Reason)
		}
	})
}
