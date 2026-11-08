package agent

import (
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
)

// compactionFileInjectionCost is the complete request cost of one key-file
// snapshot: every text part (envelope, coverage and revision attributes,
// truncation and omission notes, reload explanation) plus the calibrated token
// estimate of the whole message bundle. It is not the raw-body count in
// BuildFilePartsResult.TotalBytes, which only bounds file bodies.
type compactionFileInjectionCost struct {
	bytes  int
	tokens int
}

// fits enforces the complete-injection byte cap and the token quota together.
// A non-positive token limit means no calibrated quota is available, so only
// the byte cap applies. The estimator is an approximation of request tokens,
// not a provider-reported usage count.
func (c compactionFileInjectionCost) fits(limit compactionFileInjectionCost) bool {
	return c.bytes <= limit.bytes && (limit.tokens <= 0 || c.tokens <= limit.tokens)
}

func (p compactionReinjectionPlan) injectionCost(messages ...message.Message) compactionFileInjectionCost {
	cost := compactionFileInjectionCost{}
	for _, msg := range messages {
		for _, part := range msg.Parts {
			cost.bytes += len(part.Text) + int(part.PayloadBytes())
		}
	}
	if len(messages) > 0 {
		cost.tokens = ctxmgr.EstimateMessagesTokensWithRatio(messages, p.ratio)
	}
	return cost
}

func (p compactionReinjectionPlan) byteLimit() int {
	if p.quotaTokens <= 0 {
		return p.maxTotalBytes
	}
	bytes := p.quotaTokens * 3
	if p.ratio > 0 {
		bytes = max(1, int(float64(p.quotaTokens)/p.ratio))
	}
	return min(bytes, p.maxTotalBytes)
}

type compactionFileInjection struct {
	message message.Message
	cost    compactionFileInjectionCost
	loaded  int
	omitted int
	ok      bool
}

func (p compactionReinjectionPlan) assembleInjection(note message.ContentPart, files []message.ContentPart, omitted int) compactionFileInjection {
	parts := make([]message.ContentPart, 0, len(files)+2)
	parts = append(parts, note)
	parts = append(parts, files...)
	if omitted > 0 {
		parts = append(parts, message.ContentPart{
			Type: message.ContentPartText,
			Text: filectx.OmittedFilesNote(omitted, p.maxTotalBytes),
		})
	}
	injected := message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Parts: parts}
	cost := p.injectionCost(injected)
	return compactionFileInjection{
		message: injected, cost: cost, loaded: len(files), omitted: omitted,
		ok: cost.fits(compactionFileInjectionCost{bytes: p.maxTotalBytes, tokens: p.quotaTokens}),
	}
}

// fitCompactionFileInjection drops whole file parts from the tail first. When
// even the first file does not fit, rebuildFirst uses the same read bytes to
// shorten its body and regenerate the coverage facts. Serialized tags and
// already annotated bodies are never cut.
func fitCompactionFileInjection(note message.ContentPart, files []message.ContentPart, omitted int, plan compactionReinjectionPlan, rebuildFirst func(int) (message.ContentPart, int)) compactionFileInjection {
	if len(files) == 0 {
		return compactionFileInjection{}
	}
	var fitted compactionFileInjection
	for keep := len(files); keep > 0; keep-- {
		fitted = plan.assembleInjection(note, files[:keep], omitted+len(files)-keep)
		if fitted.ok {
			return fitted
		}
	}
	first, bodyBytes := rebuildFirst(plan.maxFileBytes)
	fitted = plan.assembleInjection(note, []message.ContentPart{first}, omitted+len(files)-1)
	if fitted.ok {
		return fitted
	}
	bodyBudget := min(plan.maxFileBytes-1, bodyBytes-max(1, fitted.cost.bytes-plan.byteLimit()))
	for bodyBudget > 0 {
		part, bodyBytes := rebuildFirst(bodyBudget)
		fitted = plan.assembleInjection(note, []message.ContentPart{part}, omitted+len(files)-1)
		if fitted.ok {
			return fitted
		}
		bodyBudget = min(bodyBudget-1, bodyBytes-max(1, fitted.cost.bytes-plan.byteLimit()))
	}
	return compactionFileInjection{}
}
