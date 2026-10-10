package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

// ToolErrorDiagnostic accompanies a terminal tool result. It never settles the agent loop.
type ToolErrorDiagnostic struct {
	Err      error
	Provider string
	Model    string
}

func imageToolErrorDiagnostic(err error) *ToolErrorDiagnostic {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	diagnostic := &ToolErrorDiagnostic{}
	if failure, ok := errors.AsType[*imagegen.Failure](err); ok {
		diagnostic.Provider, diagnostic.Model = failure.Provider, failure.Model
		message := "image request " + failure.State
		if failure.Details.Category != "" {
			message += "; " + failure.Details.String()
		}
		diagnostic.Err = &llm.APIError{StatusCode: failure.Details.HTTPStatus, Code: failure.Details.Code, Type: failure.Details.Type, Message: message}
	} else {
		diagnostic.Err = err
	}
	return diagnostic
}

// Log protocol facts rather than errors containing raw provider bodies or URLs.
func logImageRequestFailure(ctx context.Context, target imagegen.Target, elapsed time.Duration, err error) {
	failure, ok := errors.AsType[*imagegen.Failure](err)
	if !ok || failure.State == imagegen.StateNotSent || errors.Is(err, context.Canceled) {
		return
	}
	retryAfter := "unspecified"
	if failure.Details.RetryAfterSeconds != nil {
		retryAfter = strconv.FormatFloat(*failure.Details.RetryAfterSeconds, 'f', 3, 64)
	}
	request, _ := json.Marshal(failure.Details.Request)
	response, _ := json.Marshal(failure.Details.Response)
	log.Warnf("image request failed agent_id=%v call_id=%v provider=%v model=%v duration_ms=%v state=%v request_id=%v http_status=%v category=%v code=%v type=%v status=%v reason=%v param=%v message=%q request=%s response=%s retry_after_seconds=%v",
		tools.AgentIDFromContext(ctx), tools.ToolCallIDFromContext(ctx), target.Provider, target.Model, elapsed.Milliseconds(), failure.State, failure.RequestID, failure.Details.HTTPStatus, failure.Details.Category, failure.Details.Code, failure.Details.Type, failure.Details.Status, failure.Details.Reason, failure.Details.Param, failure.Details.Message, request, response, retryAfter)
}

func logImageToolFailure(event ToolResultEvent) {
	if event.Diagnostic == nil {
		return
	}
	log.Warnf("image tool failed agent_id=%v call_id=%v provider=%v model=%v duration_ms=%v error=%v", event.AgentID, event.CallID, event.Diagnostic.Provider, event.Diagnostic.Model, event.Duration.Milliseconds(), event.Diagnostic.Err)
}
