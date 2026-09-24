package llm

import (
	"maps"
	"slices"
	"strings"
)

// responsesTextPart preserves deltas until a complete text part confirms them.
type responsesTextPart struct {
	deltas strings.Builder
	text   string
	done   bool
}

type responsesItemText struct {
	parts map[int]*responsesTextPart
	text  string
	done  bool
}

// responsesItemTexts assembles text in protocol order, regardless of event
// interleaving. Authority increases from deltas to part done, item done, and
// finally the response terminal payload. Explicit empty text is authoritative.
type responsesItemTexts struct {
	byIndex map[int]*responsesItemText
}

func (t *responsesItemTexts) item(index int) *responsesItemText {
	if t.byIndex == nil {
		t.byIndex = make(map[int]*responsesItemText)
	}
	if it := t.byIndex[index]; it != nil {
		return it
	}
	it := &responsesItemText{parts: make(map[int]*responsesTextPart)}
	t.byIndex[index] = it
	return it
}

func (it *responsesItemText) part(index int) *responsesTextPart {
	if part := it.parts[index]; part != nil {
		return part
	}
	part := &responsesTextPart{}
	it.parts[index] = part
	return part
}

func (t *responsesItemTexts) appendDelta(index, contentIndex int, delta string) {
	if delta == "" {
		return
	}
	it := t.item(index)
	if it.done {
		return
	}
	part := it.part(contentIndex)
	if !part.done {
		part.deltas.WriteString(delta)
	}
}

func (t *responsesItemTexts) markPartDone(index, contentIndex int, text string) {
	it := t.item(index)
	if it.done {
		return
	}
	part := it.part(contentIndex)
	part.text, part.done = text, true
	part.deltas.Reset()
}

func (t *responsesItemTexts) markDone(index int, item responsesStreamItem) {
	text, ok := responsesOutputTextJoin(item.Content)
	if !ok {
		return
	}
	it := t.item(index)
	it.text, it.done = text, true
	clear(it.parts)
}

// join also reports presence, so an explicit empty confirmation clears earlier
// text without erasing unrelated refusal or compaction backfill when absent.
func (t *responsesItemTexts) join() (string, bool) {
	var b strings.Builder
	for _, index := range slices.Sorted(maps.Keys(t.byIndex)) {
		it := t.byIndex[index]
		if it.done {
			b.WriteString(it.text)
			continue
		}
		for _, index := range slices.Sorted(maps.Keys(it.parts)) {
			part := it.parts[index]
			if part.done {
				b.WriteString(part.text)
			} else {
				b.WriteString(part.deltas.String())
			}
		}
	}
	return b.String(), len(t.byIndex) > 0
}

// responsesOutputTextJoin concatenates the output_text parts of a message
// item's content in part order. ok reports whether any output_text part was
// present at all; a message without one did not provide text.
func responsesOutputTextJoin(blocks []responsesContentBlock) (string, bool) {
	var b strings.Builder
	ok := false
	for _, block := range blocks {
		if block.Type == "output_text" {
			ok = true
			b.WriteString(block.Text)
		}
	}
	return b.String(), ok
}

// responsesTerminalOutputText joins the output_text parts of every message in
// a completed/incomplete payload in structural order (item order, part order
// within each item). ok reports whether any output_text part was present: the
// terminal then provided the response's text — an empty text part is an
// explicit empty — so callers adopt it unconditionally.
func responsesTerminalOutputText(output []responsesOutputEntry) (string, bool) {
	var b strings.Builder
	ok := false
	for _, out := range output {
		if out.Type != "message" {
			continue
		}
		text, hasText := responsesOutputTextJoin(out.Content)
		if hasText {
			ok = true
			b.WriteString(text)
		}
	}
	return b.String(), ok
}
