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
}
