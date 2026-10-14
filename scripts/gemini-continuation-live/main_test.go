package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestObservedTransportCheckpointScenarios(t *testing.T) {
	for _, scenario := range []string{"terminal", "recover", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if requests > 1 {
					if body["continuationToken"] != "checkpoint" {
						t.Error("recovery did not use checkpoint")
					}
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"BC\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":101,\"candidatesTokenCount\":2}}\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"A\"}]},\"continuationToken\":\"checkpoint\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":1}}\n\n")
				if scenario == "recover" {
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"pending\"}]}}]}\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"B\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":2}}\n\n")
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			transport := &observedTransport{base: server.Client().Transport, cut: scenario != "cancel", cancelAtCheckpoint: scenario == "cancel", cancel: cancel}
			cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: server.URL + "/v1beta", Models: map[string]config.ModelConfig{"model-alias": {Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true)}}}}}, []string{"test-key"})
			p, err := llm.NewGeminiProviderWithClient(cfg, &http.Client{Transport: transport}, "")
			if err != nil {
				t.Fatal(err)
			}
			var preview strings.Builder
			resp, err := p.CompleteStream(ctx, "test-key", "model-alias", "", nil, nil, 100, llm.RequestTuning{}, func(d message.StreamDelta) {
				if d.Type == message.StreamDeltaRollback {
					preview.Reset()
				}
				if d.Type == message.StreamDeltaText {
					preview.WriteString(d.Text)
				}
			})
			verdict := evaluateAcceptance(transport, resp, err, preview.String())
			switch scenario {
			case "terminal":
				if err != nil || requests != 1 || transport.injected.Load() || verdict.RecoveryVerified || !verdict.PreviewMatches {
					t.Fatalf("requests=%d verdict=%+v err=%v", requests, verdict, err)
				}
			case "recover":
				if err != nil || requests != 2 || !verdict.RecoveryVerified || resp.Content != "ABC" {
					t.Fatalf("requests=%d verdict=%+v err=%v", requests, verdict, err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || !verdict.CancellationVerified || resp.Content != "A" || resp.Usage.InputTokens != 100 {
					t.Fatalf("resp=%+v verdict=%+v err=%v", resp, verdict, err)
				}
			}
		})
	}
}

func TestAcceptanceRequiresMatchingPreviewAndRecoveryRequest(t *testing.T) {
	transport := &observedTransport{}
	transport.requests.Store(2)
	transport.injected.Store(true)
	transport.faultRequest.Store(1)
	response := &message.Response{Content: "answer", Usage: &message.TokenUsage{Context: &message.ContextTokenUsage{Known: true, OutputTokens: 70000}}}
	if got := evaluateAcceptance(transport, response, nil, "wrong"); got.LongDecodeVerified || got.RecoveryVerified {
		t.Fatalf("mismatched preview accepted: %+v", got)
	}
	if got := evaluateAcceptance(transport, response, nil, "answer"); got.RecoveryVerified {
		t.Fatal("injection alone counted as recovery")
	}
	transport.recoveryRequest.Store(2)
	if got := evaluateAcceptance(transport, response, nil, "answer"); !got.RecoveryVerified || !got.LongDecodeVerified {
		t.Fatalf("valid sample rejected: %+v", got)
	}
	if got := evaluateAcceptance(transport, nil, context.DeadlineExceeded, ""); got.LongDecodeVerified || got.RecoveryVerified || got.CancellationVerified {
		t.Fatalf("timeout accepted: %+v", got)
	}
}

func TestRunRejectsInvalidSetup(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	if err := run(100, "low", "", 0, true, true, t.TempDir()); err == nil {
		t.Fatal("incompatible modes accepted")
	}
	if err := run(100, "low", "", 0, false, false, t.TempDir()); err == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("GEMINI_API_KEY", "test-key")
	if err := run(100, "low", "missing-file", 0, false, false, t.TempDir()); err == nil {
		t.Fatal("missing prompt accepted")
	}
}
