package llm

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

type anthropicNativeBlock struct {
	fields                           map[string]json.RawMessage
	input, text, thinking, signature strings.Builder
	citations                        []json.RawMessage
}
type anthropicHostedCapture struct {
	open     map[int]*anthropicNativeBlock
	complete map[int]json.RawMessage
}

// capture retains native blocks for hosted and preauthorized main requests. Complete blocks
// can be sent back on pause_turn without rebuilding or reordering their types.
func (c *anthropicHostedCapture) capture(resp *message.Response, eventType, data string) error {
	var ev struct {
		Index   int             `json:"index"`
		Block   json.RawMessage `json:"content_block"`
		Message struct {
			Container struct {
				ID string `json:"id"`
			} `json:"container"`
		} `json:"message"`
		Delta struct {
			Type        string          `json:"type"`
			Text        string          `json:"text"`
			Thinking    string          `json:"thinking"`
			Signature   string          `json:"signature"`
			PartialJSON string          `json:"partial_json"`
			Citation    json.RawMessage `json:"citation"`
			Container   struct {
				ID string `json:"id"`
			} `json:"container"`
		} `json:"delta"`
	}
	if eventType != "content_block_start" && eventType != "content_block_delta" && eventType != "content_block_stop" && eventType != "message_start" && eventType != "message_delta" {
		return nil
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("capture hosted native event: %w", err)
	}
	obs := ensureHostedObservation(resp)
	if ev.Message.Container.ID != "" {
		obs.Container = ev.Message.Container.ID
	}
	if ev.Delta.Container.ID != "" {
		obs.Container = ev.Delta.Container.ID
	}
	switch eventType {
	case "content_block_start":
		if c.open == nil {
			c.open = make(map[int]*anthropicNativeBlock)
			c.complete = make(map[int]json.RawMessage)
		}
		b := &anthropicNativeBlock{}
		if err := json.Unmarshal(ev.Block, &b.fields); err != nil {
			return fmt.Errorf("capture hosted native block: %w", err)
		}
		for field, dst := range map[string]*strings.Builder{"text": &b.text, "thinking": &b.thinking, "signature": &b.signature} {
			var value string
			if raw := b.fields[field]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &value); err != nil {
					return fmt.Errorf("capture hosted %s: %w", field, err)
				}
				dst.WriteString(value)
			}
		}
		if raw := b.fields["citations"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &b.citations); err != nil {
				return fmt.Errorf("capture hosted citations: %w", err)
			}
		}
		c.open[ev.Index] = b
	case "content_block_delta":
		if b := c.open[ev.Index]; b != nil {
			switch ev.Delta.Type {
			case "text_delta":
				b.text.WriteString(ev.Delta.Text)
			case "thinking_delta":
				b.thinking.WriteString(ev.Delta.Thinking)
			case "signature_delta":
				b.signature.WriteString(ev.Delta.Signature)
			case "input_json_delta":
				b.input.WriteString(ev.Delta.PartialJSON)
			case "citations_delta":
				if len(ev.Delta.Citation) > 0 {
					b.citations = append(b.citations, cloneHostedRaw(ev.Delta.Citation))
				}
			}
		}
	case "content_block_stop":
		b := c.open[ev.Index]
		if b == nil {
			return nil
		}
		if b.input.Len() > 0 {
			raw := json.RawMessage(b.input.String())
			if !json.Valid(raw) {
				return fmt.Errorf("hosted native input is incomplete")
			}
			b.fields["input"] = raw
		}
		for field, src := range map[string]*strings.Builder{"text": &b.text, "thinking": &b.thinking, "signature": &b.signature} {
			if _, present := b.fields[field]; present || src.Len() > 0 {
				b.fields[field], _ = json.Marshal(src.String())
			}
		}
		if b.citations != nil {
			b.fields["citations"], _ = json.Marshal(b.citations)
		}
		raw, err := json.Marshal(b.fields)
		if err != nil {
			return fmt.Errorf("marshal hosted native block: %w", err)
		}
		c.complete[ev.Index] = raw
		delete(c.open, ev.Index)
		indices := make([]int, 0, len(c.complete))
		for i := range c.complete {
			indices = append(indices, i)
		}
		slices.Sort(indices)
		obs.Items = obs.Items[:0]
		for _, i := range indices {
			obs.Items = append(obs.Items, c.complete[i])
		}
	}
	return nil
}
