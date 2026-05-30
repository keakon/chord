package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abadojack/whatlanggo"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/thinkingtranslate"
)

func (a *MainAgent) thinkingTranslationService() *thinkingtranslate.Service {
	if a == nil {
		return nil
	}
	a.thinkingTranslateMu.Lock()
	defer a.thinkingTranslateMu.Unlock()
	if a.thinkingTranslateSvc != nil {
		return a.thinkingTranslateSvc
	}

	cfg := effectiveThinkingTranslationConfig(a.globalConfig, a.projectConfig)
	if cfg == nil {
		return nil
	}
	targetLang := strings.TrimSpace(cfg.TargetLanguage)
	if targetLang == "" {
		log.Warn("thinking_translation.target_language is required; feature disabled")
		return nil
	}
	modelPool := strings.TrimSpace(cfg.ModelPool)
	if modelPool == "" {
		log.Warn("thinking_translation.model_pool is required; feature disabled")
		return nil
	}

	translator, err := a.newThinkingTranslator(modelPool)
	if err != nil {
		log.Warnf("thinking translation model pool init failed: %v", err)
		return nil
	}

	svc, err := thinkingtranslate.NewService()
	if err != nil {
		log.Warnf("thinking translation service init failed: %v", err)
		return nil
	}
	svc.TargetLang = targetLang
	svc.ModelPool = modelPool
	if cfg.MaxChars > 0 {
		svc.MaxChars = cfg.MaxChars
	}
	svc.SetTranslator(translator)
	svc.DetectLang = func(s string) (lang string, confidence float64) {
		info := whatlanggo.Detect(s)
		return strings.ToLower(info.Lang.Iso6391()), info.Confidence
	}

	a.thinkingTranslateSvc = svc
	return svc
}

func effectiveThinkingTranslationConfig(globalCfg, projectCfg *config.Config) *config.ThinkingTranslationConfig {
	if projectCfg != nil && projectCfg.ThinkingTranslation != nil {
		return projectCfg.ThinkingTranslation
	}
	if globalCfg != nil && globalCfg.ThinkingTranslation != nil {
		return globalCfg.ThinkingTranslation
	}
	return nil
}

func (a *MainAgent) newThinkingTranslator(poolName string) (*thinkingtranslate.LLMTranslator, error) {
	poolRefs, err := a.resolveConfiguredModelPool(poolName)
	if err != nil {
		return nil, err
	}
	client, err := a.newAuxModelPoolClient(poolRefs, 1*time.Minute, 2048)
	if err != nil {
		return nil, err
	}
	client.SetStreamRetryRounds(1)
	return &thinkingtranslate.LLMTranslator{NewClient: func() (*llm.Client, error) {
		return client, nil
	}}, nil
}

func (a *MainAgent) maybeTranslateLatestThinkingAfterIdle(turnID uint64) {
	if a == nil || turnID == 0 {
		return
	}
	svc := a.thinkingTranslationService()
	if svc == nil {
		return
	}

	// Ensure we schedule at most once per turn.
	a.thinkingTranslateMu.Lock()
	if a.thinkingTranslateTurnHandled == nil {
		a.thinkingTranslateTurnHandled = make(map[uint64]struct{})
	}
	if _, ok := a.thinkingTranslateTurnHandled[turnID]; ok {
		a.thinkingTranslateMu.Unlock()
		return
	}
	a.thinkingTranslateTurnHandled[turnID] = struct{}{}
	a.thinkingTranslateMu.Unlock()

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		return
	}
	lastIdx := len(msgs) - 1
	last := msgs[lastIdx]
	if last.Role != "assistant" {
		return
	}

	stableKey := fmt.Sprintf("msgidx:%d", lastIdx)
	for _, block := range extractThinkingTranslationBlocks(last) {
		a.scheduleThinkingTranslation(svc, stableKey, svc.TargetLang, block)
	}
}

func (a *MainAgent) scheduleStreamingThinkingTranslation(messageIndex, blockIndex int, original string) {
	if a == nil || strings.TrimSpace(original) == "" || messageIndex < 0 || blockIndex < 0 {
		return
	}
	svc := a.thinkingTranslationService()
	if svc == nil {
		return
	}
	messageKey := fmt.Sprintf("msgidx:%d", messageIndex)
	a.scheduleThinkingTranslation(svc, messageKey, svc.TargetLang, thinkingTranslationBlock{BlockIndex: blockIndex, Original: original})
}

func (a *MainAgent) resetThinkingTranslationSeen() {
	if a == nil {
		return
	}
	a.thinkingTranslateMu.Lock()
	a.thinkingTranslateSeen = nil
	a.thinkingTranslateTurnHandled = nil
	a.thinkingTranslateMu.Unlock()
}

func (a *MainAgent) scheduleThinkingTranslation(svc *thinkingtranslate.Service, messageKey, userLang string, block thinkingTranslationBlock) {
	if a == nil || svc == nil || strings.TrimSpace(block.Original) == "" {
		return
	}
	translationCtx := a.parentCtx
	if translationCtx == nil {
		translationCtx = context.Background()
	}
	sessionEpoch := a.sessionEpoch
	seenKey := fmt.Sprintf("%d:%s:%d", sessionEpoch, messageKey, block.BlockIndex)
	a.thinkingTranslateMu.Lock()
	if a.thinkingTranslateSeen == nil {
		a.thinkingTranslateSeen = make(map[string]struct{})
	}
	if _, ok := a.thinkingTranslateSeen[seenKey]; ok {
		a.thinkingTranslateMu.Unlock()
		return
	}
	a.thinkingTranslateSeen[seenKey] = struct{}{}
	a.thinkingTranslateMu.Unlock()
	a.translateThinkingBlockAsync(translationCtx, sessionEpoch, svc, messageKey, userLang, block)
}

type thinkingTranslationBlock struct {
	BlockIndex int
	Original   string
}

func extractThinkingTranslationBlocks(msg message.Message) []thinkingTranslationBlock {
	blocks := make([]thinkingTranslationBlock, 0, len(msg.ThinkingBlocks))
	if len(msg.ThinkingBlocks) > 0 {
		for i, block := range msg.ThinkingBlocks {
			if strings.TrimSpace(block.Thinking) == "" {
				continue
			}
			blocks = append(blocks, thinkingTranslationBlock{BlockIndex: i, Original: block.Thinking})
		}
		return blocks
	}
	if strings.TrimSpace(msg.ReasoningContent) != "" {
		blocks = append(blocks, thinkingTranslationBlock{BlockIndex: 0, Original: msg.ReasoningContent})
	}
	return blocks
}

func (a *MainAgent) translateThinkingBlockAsync(ctx context.Context, sessionEpoch uint64, svc *thinkingtranslate.Service, messageKey, userLang string, block thinkingTranslationBlock) {
	if strings.TrimSpace(block.Original) == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	original := block.Original
	blockIndex := block.BlockIndex
	a.outputWg.Go(func() {
		trigger, meta := svc.ShouldTranslate(userLang, original)
		if !trigger {
			log.Debugf("thinking translation skipped message=%s block=%d reason=%s detected=%s confidence=%.2f latin_ratio=%.2f", messageKey, blockIndex, meta.Reason, meta.DetectedLang, meta.Confidence, meta.LatinRatio)
			return
		}

		started := time.Now()
		out, err := svc.TranslateText(ctx, original, &meta)
		meta.Duration = time.Since(started)
		if err != nil {
			log.Debugf("thinking translation failed message=%s block=%d pool=%s target=%s reason=%s duration=%s err=%v", messageKey, blockIndex, svc.ModelPool, meta.TargetLang, meta.Reason, meta.Duration, err)
			return
		}
		if thinkingtranslate.NormalizeForCompare(out) == thinkingtranslate.NormalizeForCompare(original) {
			log.Debugf("thinking translation ignored identical output message=%s block=%d pool=%s target=%s duration=%s chunks=%d", messageKey, blockIndex, svc.ModelPool, meta.TargetLang, meta.Duration, meta.Chunks)
			return
		}
		if ctx.Err() != nil || a.sessionEpoch != sessionEpoch {
			log.Debugf("thinking translation dropped stale event message=%s block=%d session_epoch=%d current_session_epoch=%d", messageKey, blockIndex, sessionEpoch, a.sessionEpoch)
			return
		}
		if err := recovery.SaveThinkingTranslation(a.sessionDir, recovery.ThinkingTranslationEntry{
			AgentID:      "",
			MessageID:    messageKey,
			BlockIndex:   blockIndex,
			TargetLang:   meta.TargetLang,
			OriginalHash: recovery.ThinkingTranslationOriginalHash(original),
			Translated:   out,
		}); err != nil {
			log.Debugf("thinking translation persist failed message=%s block=%d err=%v", messageKey, blockIndex, err)
		}
		a.emitToTUI(ThinkingTranslatedEvent{AgentID: "", MessageID: messageKey, BlockIndex: blockIndex, Translated: out, TargetLang: meta.TargetLang})
	})
}
