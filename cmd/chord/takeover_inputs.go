package main

import (
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

const (
	headlessPausedInputLimit       = 8
	headlessPausedInputIDBytes     = 128
	headlessPausedInputPreviewSize = 160
)

type headlessPausedInputsBackend interface {
	CurrentSessionPauseStatus() agent.SessionPauseStatus
	PendingSessionInputs() []recovery.PendingInput
}

type headlessPausedInputs struct {
	Paused            bool                         `json:"paused"`
	PendingInputCount int                          `json:"pending_input_count"`
	Inputs            []headlessPausedInputSummary `json:"inputs"`
}

type headlessPausedInputSummary struct {
	InputID         string `json:"input_id"`
	OwnerTaskID     string `json:"owner_task_id"`
	Preview         string `json:"preview"`
	AttachmentCount int    `json:"attachment_count"`
}

// headlessPausedInputProjection reads the owning runtime's current pause state.
// Missing or invalid data stays distinct from a confirmed empty queue.
func headlessPausedInputProjection(backend any) *headlessPausedInputs {
	reader, ok := backend.(headlessPausedInputsBackend)
	if !ok {
		return nil
	}
	value := reflect.ValueOf(reader)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	inputs := reader.PendingSessionInputs()
	status := reader.CurrentSessionPauseStatus()
	out := &headlessPausedInputs{PendingInputCount: len(inputs), Inputs: make([]headlessPausedInputSummary, 0, min(len(inputs), headlessPausedInputLimit))}
	switch status.State {
	case agent.SessionPauseReady, agent.SessionPauseDraining, agent.SessionPauseSaving, agent.SessionPauseSaveFailed, agent.SessionPauseSaved, agent.SessionPauseReleased:
		out.Paused = true
	}
	seen := make(map[string]struct{}, min(len(inputs), headlessPausedInputLimit))
	for _, input := range inputs[:min(len(inputs), headlessPausedInputLimit)] {
		_, duplicate := seen[input.InputID]
		if !validHeadlessPausedInputID(input.InputID) || !validHeadlessPausedInputID(input.OwnerTaskID) || duplicate {
			log.Warn("paused input projection has invalid durable identity")
			return nil
		}
		seen[input.InputID] = struct{}{}
		summary := headlessPausedInputSummary{InputID: input.InputID, OwnerTaskID: input.OwnerTaskID, Preview: headlessInputPreview(input)}
		for _, part := range input.Parts {
			if part.IsBinary() {
				summary.AttachmentCount++
			}
		}
		out.Inputs = append(out.Inputs, summary)
	}
	return out
}

func validHeadlessPausedInputID(id string) bool {
	if id == "" || len(id) > headlessPausedInputIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func headlessInputPreview(input recovery.PendingInput) string {
	var out strings.Builder
	count := 0
	space := false
	appendText := func(text string) {
		for _, r := range text {
			if count >= headlessPausedInputPreviewSize {
				return
			}
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				space = count > 0
				continue
			}
			if space {
				out.WriteByte(' ')
				count++
				space = false
				if count >= headlessPausedInputPreviewSize {
					return
				}
			}
			out.WriteRune(r)
			count++
		}
	}
	if input.Content != "" {
		appendText(input.Content)
	} else {
		for _, part := range input.Parts {
			if part.Type == message.ContentPartText {
				text := part.DisplayText
				if text == "" {
					text = part.Text
				}
				appendText(text)
				space = count > 0
				if count >= headlessPausedInputPreviewSize {
					break
				}
			}
		}
	}
	return strings.TrimSpace(out.String())
}
