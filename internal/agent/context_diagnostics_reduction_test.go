package agent

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func TestDiagnosticReductionStartsAfterRequestProjection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []message.Message
	}{
		{"images", []message.Message{{Role: message.RoleUser, Content: "sample"}, {Role: message.RoleTool, ToolCallID: "sample", Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "sample"}, {Type: message.ContentPartImage, ArtifactID: "sample-a"}, {Type: message.ContentPartImage, ArtifactID: "sample-b"}}}}},
		{"checkpoint", []message.Message{requestViewFixture(), {Role: message.RoleUser, Content: "Continue."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &MainAgent{ctxMgr: ctxmgr.NewManager(8192, 0)}
			original, _ := json.Marshal(tc.messages)
			projected := projectCheckpointRequests(projectGeneratedImagePreviews(tc.messages))
			if reflect.DeepEqual(projected, tc.messages) {
				t.Fatal("fixture must change during projection")
			}
			result := a.prepareMessagesForLLM(tc.messages)
			r := a.diagnosticReduction()
			want := int64(ctxmgr.EstimateMessagesTokensWithRatio(projected, r.Ratio))
			if r.State != contextdiag.Observed || r.Before != want || r.After != want {
				t.Fatalf("reduction=%+v; projected estimate=%d", r, want)
			}
			if !reflect.DeepEqual(result, projected) {
				t.Fatal("unexpected reduction in projection-only fixture")
			}
			current, _ := json.Marshal(tc.messages)
			if string(original) != string(current) {
				t.Fatal("diagnostics modified durable input")
			}
		})
	}
}

func TestDiagnosticReductionResourceDegradation(t *testing.T) {
	nativeItems := make([]json.RawMessage, contextdiag.MaxFragments+1)
	for i := range nativeItems {
		nativeItems[i] = json.RawMessage(`{"type":"message"}`)
	}
	for _, tc := range []struct {
		name     string
		messages []message.Message
		reason   string
	}{
		{"native_items", []message.Message{{NativeTools: &message.NativeToolHistory{Items: nativeItems}}}, diagnosticReductionOpaqueNative},
		{"native_bytes", []message.Message{{NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{json.RawMessage(strings.Repeat("x", 1<<20))}}}}, diagnosticReductionOpaqueNative},
		{"messages", make([]message.Message, contextdiag.MaxMessages+1), diagnosticReductionScanIncomplete},
		{"parts", []message.Message{{Parts: make([]message.ContentPart, contextdiag.MaxFragments+1)}}, diagnosticReductionScanIncomplete},
		{"nested_summary", []message.Message{{ResponsesOutput: []message.ResponsesOutputItem{{Summary: make([]message.ResponsesReasoningSummary, contextdiag.MaxFragments+1)}}}}, diagnosticReductionScanIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &MainAgent{}
			a.freezeDiagnosticReduction([]message.Message{{Content: "sample"}}, nil, 0.5)
			for _, ratio := range []float64{0, 0.5} {
				a.freezeDiagnosticReduction(tc.messages, tc.messages, ratio)
				r := a.diagnosticReduction()
				if r.State != contextdiag.Unavailable || !slices.Contains(r.Reasons, tc.reason) || r.Before != 0 || r.After != 0 {
					t.Fatalf("degraded observation=%+v", r)
				}
			}
		})
	}
	b := diagnosticReductionBudget{deadline: time.Now().Add(-time.Second)}
	if b.check([]message.Message{{Content: "sample"}}) != diagnosticReductionScanIncomplete {
		t.Fatal("expired budget accepted")
	}
	b = diagnosticReductionBudget{deadline: time.Now().Add(time.Minute)}
	face := []message.Message{{Parts: make([]message.ContentPart, contextdiag.MaxFragments/2)}}
	if b.check(face) != "" || b.check(face) != diagnosticReductionScanIncomplete {
		t.Fatal("request faces did not share fragment budget")
	}
}

func BenchmarkDiagnosticReduction(b *testing.B) {
	for _, native := range []bool{false, true} {
		name := "ordinary"
		if native {
			name = "native"
		}
		b.Run(name, func(b *testing.B) {
			a := &MainAgent{}
			messages := make([]message.Message, 500)
			for i := range messages {
				messages[i] = message.Message{Role: message.RoleUser, Content: strings.Repeat("x", 2048)}
			}
			if native {
				messages[0].NativeTools = &message.NativeToolHistory{Items: []json.RawMessage{json.RawMessage(strings.Repeat("x", 1<<20))}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				a.freezeDiagnosticReduction(messages, messages, 0.3)
			}
		})
	}
}
