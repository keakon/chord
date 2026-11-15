package agent

import (
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
)

// reductionPartsForRequest returns the parts the image-eviction rule may
// inspect. Native tool history replays its own image surface from NativeTools
// at request time; its message parts are mirrors, not the request payload, so
// they stay out of the eviction decision. Whether native replay should skip
// or follow the eviction verdict is a separate decision that does not belong
// to this parts rule.
func reductionPartsForRequest(msg *message.Message) []message.ContentPart {
	if msg.NativeTools != nil {
		return nil
	}
	return msg.Parts
}

func toolResultTextReduced(original, prepared message.Message) bool {
	return prepared.Content != original.Content || prepared.ToolDiff != original.ToolDiff
}

func (rp *reductionPass) proposeImageEviction(index int, ctx requesttrim.Context, verdict requesttrim.Verdict) bool {
	if !verdict.EvictImages {
		return false
	}
	marker, ok := requesttrim.RenderImageEviction(ctx)
	if !ok {
		return false
	}
	verdict.Class = requesttrim.None
	rp.proposals = append(rp.proposals, reductionProposal{
		index:       index,
		toolName:    ctx.ToolName,
		rule:        string(requesttrim.ImageOutput),
		imageMarker: marker,
		evictImages: true,
		decision:    requesttrim.RetentionDecision(ctx, verdict, "", ""),
	})
	return true
}

// rebuildToolResultPartsAfterImageEviction rebuilds a tool result's parts with
// its image parts replaced by one text part carrying the eviction marker. The
// marker lands where the first evicted image stood, so text, PDF and remaining
// parts keep their order; the leading text part is re-synced to the final
// content so the wire surface (which renders parts and ignores Content when
// parts exist) reflects any string reduction applied to the same message.
//
// It returns nil when there is nothing to change: no image parts means no
// rebuild, and callers must not replace msg.Parts in that case.
func rebuildToolResultPartsAfterImageEviction(parts []message.ContentPart, text, marker string) []message.ContentPart {
	firstImage := -1
	images := 0
	for i := range parts {
		if parts[i].Type == message.ContentPartImage {
			if firstImage < 0 {
				firstImage = i
			}
			images++
		}
	}
	if firstImage < 0 {
		return nil
	}
	rebuilt := make([]message.ContentPart, 0, len(parts)-images+1)
	for i := range parts {
		if i == firstImage {
			rebuilt = append(rebuilt, message.ContentPart{Type: message.ContentPartText, Text: marker})
		}
		if parts[i].Type == message.ContentPartImage {
			continue
		}
		rebuilt = append(rebuilt, parts[i])
	}
	// The wire renders parts and ignores Content when parts exist, so the text
	// part must mirror the final content — including a string reduction applied
	// alongside the eviction. toolResultParts builds image-bearing results with
	// the text mirror first; respect that layout when it holds.
	if firstImage > 0 && text != "" && len(rebuilt) > 0 && rebuilt[0].Type == message.ContentPartText && rebuilt[0].Text != text {
		rebuilt[0].Text = text
	}
	return rebuilt
}

// syncToolResultPartsText re-syncs the leading text part of a parts-bearing
// tool result with its final content, so a string reduction reaches the wire
// surface on messages whose text is mirrored into parts. It returns nil when
// the parts need no rewrite.
func syncToolResultPartsText(parts []message.ContentPart, text string) []message.ContentPart {
	if text == "" || len(parts) == 0 || parts[0].Type != message.ContentPartText || parts[0].Text == text {
		return nil
	}
	synced := make([]message.ContentPart, len(parts))
	copy(synced, parts)
	synced[0].Text = text
	return synced
}

// applyProposal writes one proposal onto a prepared message. It is the single
// application point shared by the cache-amortization estimator and the apply
// pass, so the token estimate never disagrees with what apply writes. Nested
// slices are only ever replaced wholesale — no part element or map entry is
// mutated in place — so the caller's shallow message copies stay safe.
func (rp *reductionPass) applyProposal(msg *message.Message, p reductionProposal) {
	if p.reduced != "" {
		msg.Content = p.reduced
		if p.class != requesttrim.Diagnostics {
			msg.ToolDiff = ""
		}
	}
	if p.evictImages {
		if rebuilt := rebuildToolResultPartsAfterImageEviction(msg.Parts, msg.Content, p.imageMarker); rebuilt != nil {
			msg.Parts = rebuilt
		}
	} else if p.reduced != "" {
		if synced := syncToolResultPartsText(msg.Parts, msg.Content); synced != nil {
			msg.Parts = synced
		}
	}
}

// proposalAppliedMessage returns the message a proposal would produce, for
// full-message token estimation on the amortization path.
func (rp *reductionPass) proposalAppliedMessage(p reductionProposal) message.Message {
	applied := rp.prepared[p.index]
	rp.applyProposal(&applied, p)
	return applied
}
