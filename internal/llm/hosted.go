package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// ensureHostedObservation returns the response's hosted tool observation,
// creating it on first capture.
func ensureHostedObservation(resp *message.Response) *message.HostedObservation {
	if resp.Hosted == nil {
		resp.Hosted = &message.HostedObservation{}
	}
	return resp.Hosted
}

// finalizeHostedObservation stamps the fields that only exist once the stream
// is assembled: the model summary text and the normalized usage.
func finalizeHostedObservation(resp *message.Response) {
	if resp == nil || resp.Hosted == nil {
		return
	}
	if resp.Hosted.Summary == "" {
		resp.Hosted.Summary = cloneLongLivedLLMString(resp.Content)
	}
	if resp.Hosted.Usage == nil {
		resp.Hosted.Usage = resp.Usage
	}
}

// hostedSubRequestText returns the user text of a hosted sub-request. The
// hosted declaration travels in tuning rather than in the message history, so
// the sub-request's only message is the query; the last user message wins.
func hostedSubRequestText(messages []message.Message) string {
	for _, msg := range slices.Backward(messages) {
		if msg.Role != message.RoleUser {
			continue
		}
		if text := strings.TrimSpace(msg.Content); text != "" {
			return text
		}
		var b strings.Builder
		for _, part := range msg.Parts {
			if part.Type == message.ContentPartText {
				b.WriteString(part.Text)
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

// cloneHostedRaw copies a raw wire payload retained by a hosted observation.
// The parser's payload slices may alias larger decode buffers while the
// observation outlives the parse call.
func cloneHostedRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// HostedOutcomeUnknownError stops automatic replay of a hosted operation when
// the provider may have executed it. Response retains any available usage.
type HostedOutcomeUnknownError struct {
	Cause    error
	Response *message.Response
}

func (e *HostedOutcomeUnknownError) Error() string {
	return fmt.Sprintf("hosted execution outcome unknown; automatic replay stopped: %v", e.Cause)
}
func (e *HostedOutcomeUnknownError) Unwrap() error { return e.Cause }
func hostedRequestRejected(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && !apiErr.isStreamEvent() && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}
