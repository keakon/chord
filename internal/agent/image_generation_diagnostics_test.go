package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/tools"
)

func TestImageFailureDiagnosticsLogSafeProtocolFacts(t *testing.T) {
	var output bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&output, golog.WarnLevel))
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	failure := &imagegen.Failure{Provider: "sample", Model: "gpt-image-1.5", State: imagegen.StateUnknown, Details: imagegen.FailureDetails{Category: imagegen.FailureProvider, HTTPStatus: 502}, Cause: fmt.Errorf("private-token https://example.invalid/?signature=private-signature raw provider body")}
	diagnostic := imageToolErrorDiagnostic(fmt.Errorf("operation failed: %w", failure))
	if diagnostic == nil || diagnostic.Provider != "sample" || diagnostic.Model != "gpt-image-1.5" {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	apiError, ok := errors.AsType[*llm.APIError](diagnostic.Err)
	if !ok || apiError.StatusCode != 502 || !strings.Contains(apiError.Message, imagegen.StateUnknown) {
		t.Fatalf("error=%v", diagnostic.Err)
	}
	ctx := tools.WithToolCallID(t.Context(), "call-1")
	logImageRequestFailure(ctx, imagegen.Target{Provider: "sample", Model: "gpt-image-1.5"}, 30*time.Second, failure)
	logImageToolFailure(ToolResultEvent{CallID: "call-1", Diagnostic: diagnostic, Duration: 30 * time.Second})
	text := output.String() + diagnostic.Err.Error()
	for _, want := range []string{"image request failed", "image tool failed", "http_status=502", "provider=sample", "model=gpt-image-1.5", "call_id=call-1", "duration_ms=30000", "retry_after_seconds=unspecified"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	for _, secret := range []string{"private-token", "private-signature", "raw provider body", "example.invalid"} {
		if strings.Contains(text, secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
}

func TestImageDiagnosticsIgnoreCancellationAndSuccess(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, &imagegen.Failure{State: imagegen.StateUnknown, Cause: context.Canceled}} {
		if got := imageToolErrorDiagnostic(err); got != nil {
			t.Fatalf("unexpected diagnostic=%+v", got)
		}
	}
	local := fmt.Errorf("operation is outcome_unknown; cannot recover a download")
	if got := imageToolErrorDiagnostic(local); got == nil || got.Err != local {
		t.Fatal("local recovery failure lost")
	}
}

func TestImageFailureTerminalCarriesMainAndSubDiagnostics(t *testing.T) {
	failure := &imagegen.Failure{Provider: "sample", Model: "gpt-image-1.5", State: imagegen.StateUnknown, Details: imagegen.FailureDetails{Category: imagegen.FailureProvider, HTTPStatus: 502}, Cause: fmt.Errorf("HTTP 502")}
	for _, subCaller := range []bool{false, true} {
		t.Run(fmt.Sprint(subCaller), func(t *testing.T) {
			var parent *MainAgent
			if subCaller {
				var sub *SubAgent
				parent, sub = newMixedBatchTestSubAgent(t)
				sub.turn.PendingToolCalls.Store(2)
				sub.handleToolResult(&toolResult{CallID: "image-call", Name: tools.NameGenerateImage, TurnID: sub.turn.ID, Error: failure})
			} else {
				parent = newReadyTestMainAgent(t)
				parent.newTurn()
				parent.turn.PendingToolCalls.Store(2)
				parent.handleToolResult(Event{Type: EventToolResult, TurnID: parent.turn.ID, Payload: &ToolResultPayload{CallID: "image-call", Name: tools.NameGenerateImage, TurnID: parent.turn.ID, Error: failure}})
			}
			parent.flushPersist()
			found := false
			for _, event := range drainAgentEvents(parent.Events()) {
				if result, ok := event.(ToolResultEvent); ok && result.CallID == "image-call" {
					found = true
					if result.Status != ToolResultStatusError || result.Diagnostic == nil || result.Diagnostic.Provider != "sample" || result.Diagnostic.Model != "gpt-image-1.5" {
						t.Fatalf("event=%+v", result)
					}
				}
				if _, ok := event.(ErrorEvent); ok {
					t.Fatal("tool failure became an agent-loop error")
				}
			}
			if !found {
				t.Fatal("image terminal missing")
			}
		})
	}
}

func TestImageFailureLogIncludesUploadAndRejectionDetails(t *testing.T) {
	var output bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&output, golog.WarnLevel))
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	failure := &imagegen.Failure{State: imagegen.StateRejected, RequestID: "request-1", Cause: fmt.Errorf("HTTP 400"), Details: imagegen.FailureDetails{
		Category: imagegen.FailureInvalidRequest, HTTPStatus: 400, Code: "unsupported_file_mimetype", Param: "image[0]", Message: "Unsupported image MIME",
		Request:  &imagegen.RequestDiagnostics{Operation: imagegen.Edit, Endpoint: "/v1/images/edits", Images: []imagegen.ReferenceDiagnostics{{Field: "image[]", MIME: "image/png", PartMIME: "application/octet-stream", Bytes: 100}}},
		Response: &imagegen.ResponseDiagnostics{ContentType: "application/json", Bytes: 200, CFRay: "abcd-TEST"},
	}}
	logImageRequestFailure(tools.WithToolCallID(t.Context(), "call-1"), imagegen.Target{Provider: "sample", Model: "gpt-image-1"}, time.Second, failure)
	for _, want := range []string{"code=unsupported_file_mimetype", "param=image[0]", `message="Unsupported image MIME"`, `"operation":"edit"`, `"endpoint":"/v1/images/edits"`, `"mime_type":"image/png"`, `"part_mime_type":"application/octet-stream"`, `"cf_ray":"abcd-TEST"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, output.String())
		}
	}
	diagnostic := imageToolErrorDiagnostic(failure)
	if !strings.Contains(diagnostic.Err.Error(), "Unsupported image MIME") || !strings.Contains(diagnostic.Err.Error(), "param=image[0]") {
		t.Fatalf("panel error=%v", diagnostic.Err)
	}
}
