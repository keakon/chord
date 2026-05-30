package thinkingtranslate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
)

var (
	errEmptyTranslationResponse   = errors.New("empty translation response")
	errInvalidTranslationResponse = errors.New("invalid translation response")
)

type LLMTranslator struct {
	NewClient func() (*llm.Client, error)
	mu        sync.Mutex
}

func (t *LLMTranslator) TranslateChunk(ctx context.Context, targetLang, chunk string) (string, error) {
	if t == nil || t.NewClient == nil {
		return "", fmt.Errorf("llm translator not configured")
	}
	client, err := t.NewClient()
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", fmt.Errorf("llm translator returned nil client")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	pool, cursor := client.ModelPoolSnapshot()
	if len(pool) == 0 {
		return t.translateOnce(ctx, client, targetLang, chunk)
	}

	start := time.Now()
	var lastErr error
	for i := range pool {
		idx := (cursor + i) % len(pool)
		client.SetModelPool(pool, idx)
		selected := pool[idx]
		translated, err := t.translateOnce(ctx, client, targetLang, chunk)
		if err == nil {
			if i > 0 {
				log.Debugf("thinking translation fallback succeeded target=%s model=%s/%s variant=%s attempts=%d duration=%s", targetLang, selected.ProviderConfig.Name(), selected.ModelID, selected.Variant, i+1, time.Since(start))
			}
			return translated, nil
		}
		lastErr = err
		if errors.Is(err, errEmptyTranslationResponse) {
			log.Debugf("thinking translation empty response target=%s model=%s/%s variant=%s attempts=%d duration=%s", targetLang, selected.ProviderConfig.Name(), selected.ModelID, selected.Variant, i+1, time.Since(start))
			continue
		}
		if i < len(pool)-1 {
			log.Debugf("thinking translation fallback retrying on next model target=%s model=%s/%s variant=%s attempts=%d duration=%s err=%v", targetLang, selected.ProviderConfig.Name(), selected.ModelID, selected.Variant, i+1, time.Since(start), err)
			continue
		}
		log.Debugf("thinking translation fallback failed target=%s model=%s/%s variant=%s attempts=%d duration=%s err=%v", targetLang, selected.ProviderConfig.Name(), selected.ModelID, selected.Variant, i+1, time.Since(start), err)
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", errEmptyTranslationResponse
}

func (t *LLMTranslator) translateOnce(ctx context.Context, client *llm.Client, targetLang, chunk string) (string, error) {
	client.SetSystemPrompt(translationSystemPrompt)
	client.SetOutputTokenMax(2048)
	resp, err := client.CompleteStream(ctx, translationPrompt(targetLang, chunk), nil, nil)
	if err != nil {
		return "", err
	}
	translated := ExtractTranslationEnvelope(resp.Content)
	if translated == "" {
		if len(resp.ThinkingBlocks) > 0 {
			translated = ExtractTranslationEnvelope(resp.ThinkingBlocks[len(resp.ThinkingBlocks)-1].Thinking)
		}
		if translated == "" {
			translated = ExtractTranslationEnvelope(resp.ReasoningContent)
		}
	}
	if translated == "" {
		return "", errEmptyTranslationResponse
	}
	if IsClearlyInvalidTranslation(chunk, targetLang, translated) {
		return "", errInvalidTranslationResponse
	}
	return translated, nil
}
