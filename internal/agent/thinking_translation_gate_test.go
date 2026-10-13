package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/llm"
)

func TestThinkingTranslationSessionSwitchRebindsGate(t *testing.T) {
	oldGate := &executiongate.Gate{}
	a := &MainAgent{
		executionGate: oldGate,
		projectConfig: &config.Config{
			ThinkingTranslation: &config.ThinkingTranslationConfig{TargetLanguage: "en", ModelPool: "translation"},
			ModelPools:          map[string][]string{"translation": {"sample/test-model"}},
		},
	}
	a.modelSwitchFactory = func(_ string, _ []string, _ string) (*llm.Client, string, int, error) {
		return newAuxModelPoolTestClient("sample", "test-model"), "sample/test-model", 0, nil
	}
	oldService := a.thinkingTranslationService()
	if oldService == nil {
		t.Fatal("translation service unavailable")
	}
	oldGate.Freeze()
	a.executionGate = &executiongate.Gate{}
	a.resetThinkingTranslationSeen()
	newService := a.thinkingTranslationService()
	if newService == nil || newService == oldService {
		t.Fatal("session switch retained translator bound to previous gate")
	}
	// A queued translation from the old session still cannot dispatch, while
	// the new session dispatches through its own gate.
	if _, err := oldService.TranslateText(context.Background(), "Sample source.", nil); !executiongate.IsPaused(err) {
		t.Fatalf("old session translation err=%v, want paused", err)
	}
	if _, err := newService.TranslateText(context.Background(), "Sample source.", nil); executiongate.IsPaused(err) {
		t.Fatalf("new session translation used frozen old gate: %v", err)
	}
	a.executionGate.Freeze()
	if _, err := newService.TranslateText(context.Background(), "Sample source.", nil); !executiongate.IsPaused(err) {
		t.Fatalf("new session translation bypassed frozen gate: %v", err)
	}
}
