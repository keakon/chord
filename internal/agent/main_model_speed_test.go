package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestFocusedModelSpeedUsesRecordedRequestRef(t *testing.T) {
	for _, tc := range []struct {
		name     string
		worker   bool
		fallback bool
	}{
		{name: "main variant"},
		{name: "main fallback variant", fallback: true},
		{name: "worker variant", worker: true},
		{name: "worker fallback variant", worker: true, fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newReadyTestMainAgent(t)
			provider := llm.NewProviderConfig("provider-a", config.ProviderConfig{
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"model-1": {
						Limit:    config.ModelLimit{Context: 128000, Output: 4096},
						Variants: map[string]config.ModelVariant{"high": {}},
					},
				},
			}, []string{"test-key"})
			success := scriptedStreamCall{
				streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "reply"}},
				resp: &message.Response{
					Content:    "reply",
					StopReason: "stop",
					Usage:      &message.TokenUsage{InputTokens: 100, OutputTokens: 50},
				},
			}
			primary := &blockingStreamProvider{calls: []scriptedStreamCall{success}}
			if tc.fallback {
				primary.calls = []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "unavailable"}}}
			}
			client := llm.NewClient(provider, primary, "model-1", 4096, "system")
			client.SetVariant("high")
			wantRef := "provider-a/model-1@high"
			if tc.fallback {
				fallback := llm.NewProviderConfig("provider-b", config.ProviderConfig{
					Type: config.ProviderTypeChatCompletions,
					Models: map[string]config.ModelConfig{
						"model-1": {
							Limit:    config.ModelLimit{Context: 128000, Output: 4096},
							Variants: map[string]config.ModelVariant{"low": {}},
						},
					},
				}, []string{"test-key"})
				client.SetFallbackModels([]llm.FallbackModel{{
					ProviderConfig: fallback,
					ProviderImpl:   &blockingStreamProvider{calls: []scriptedStreamCall{success}},
					ModelID:        "model-1",
					MaxTokens:      4096,
					Variant:        "low",
				}})
				wantRef = "provider-b/model-1@low"
			}

			events := make(chan analytics.UsageEvent, 1)
			a.SetUsageEventSink(func(event analytics.UsageEvent) {
				if event.Purpose == analytics.UsagePurposeChat {
					select {
					case events <- event:
					default:
						t.Errorf("unexpected extra chat usage event: %+v", event)
					}
				}
			})
			input := message.Message{Role: message.RoleUser, Content: "hello"}
			var sub *SubAgent
			var resp *message.Response
			agentID := identity.MainAgentID
			if tc.worker {
				a.SetAgentConfigs(map[string]*config.AgentConfig{
					"worker": {
						Name:    "worker",
						Mode:    config.AgentModeSubAgent,
						Models:  map[string][]string{"default": {"provider-a/model-1"}},
						Variant: "high",
					},
				})
				sub = newControllableTestSubAgent(t, a, "task-speed")
				sub.llmClient.Close()
				sub.llmClient = client
				prepareSubRequestDirectoryFixture(t, sub)
				sub.ctxMgr.Append(input)
				turn := sub.newTurn()
				sub.asyncCallLLMWithFlightMarked(turn)
				result := waitForSubAgentLLMResult(t, sub, 5*time.Second)
				joined := make(chan struct{})
				go func() {
					sub.llmWG.Wait()
					close(joined)
				}()
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Fatal("timed out joining the worker request")
				}
				sub.finishLLMRequest()
				if result.err != nil {
					t.Fatalf("worker request: %v", result.err)
				}
				resp = result.resp
				agentID = sub.instanceID
				a.SwitchFocus(agentID)
			} else {
				a.swapLLMClientWithRef(client, "model-1", 128000, client.NextRequestModelRef())
				ctx, cancel := context.WithTimeout(a.parentCtx, 5*time.Second)
				defer cancel()
				var err error
				resp, err = a.callLLMForRequest(ctx, llmRequestInput{Messages: []message.Message{input}}, 0)
				if err != nil {
					t.Fatalf("main request: %v", err)
				}
			}

			var event analytics.UsageEvent
			select {
			case event = <-events:
			case <-time.After(time.Second):
				t.Fatal("missing request usage event")
			}
			if event.AgentID != agentID || event.RunningModelRef != wantRef {
				t.Fatalf("request attribution = %q/%q, want %q/%q", event.AgentID, event.RunningModelRef, agentID, wantRef)
			}
			if event.RequestNs <= 0 || event.RequestNs != resp.RequestNs || event.FirstOutputNs <= 0 {
				t.Fatalf("request timing = %+v, response = %+v", event, resp)
			}
			state := a.FocusedModelState()
			if state.DisplayRef != wantRef {
				t.Fatalf("settled display = %q, want successful sticky target %q", state.DisplayRef, wantRef)
			}
			stats := a.GetSidebarUsageStats()
			model := stats.ByModel[state.DisplayRef]
			if model == nil || model.ThroughputTokens != 50 || model.ThroughputSamples != 1 ||
				model.ThroughputNs != event.RequestNs || model.FirstOutputTotalNs != event.FirstOutputNs || model.FirstOutputSamples != 1 {
				t.Fatalf("sidebar model %q = %+v, want the recorded request sample", state.DisplayRef, model)
			}
			if len(stats.ByModel) != 1 {
				t.Fatalf("sidebar models = %+v, want only the successful target", stats.ByModel)
			}
			if !tc.worker {
				// An idle display follows the next target, not necessarily the
				// model that produced the last usage event.
				a.swapLLMClientWithRef(newTestLLMClient(), "test-model", 8192, "test/test-model")
				if ref := a.FocusedModelState().DisplayRef; a.GetSidebarUsageStats().ByModel[ref] != nil {
					t.Fatalf("unmeasured next model %q inherited the previous model's sample", ref)
				}
				return
			}
			if event.TaskID != sub.taskID {
				t.Fatalf("usage task = %q, want %q", event.TaskID, sub.taskID)
			}
			sub.setState(SubAgentStateCompleted, "done")
			a.syncTaskRecordFromSub(sub, "")
			if !a.parkSubAgent(sub.instanceID) {
				t.Fatal("parkSubAgent() = false")
			}
			if got := a.FocusedModelState().DisplayRef; got != wantRef {
				t.Fatalf("parked display = %q, want %q", got, wantRef)
			}
			if got := a.GetSidebarUsageStats(); !reflect.DeepEqual(got, stats) {
				t.Fatalf("parked stats = %+v, want live stats %+v", got, stats)
			}
			if err := a.recoveryManager().PersistMessage(identity.MainAgentID, input); err != nil {
				t.Fatalf("persist main transcript: %v", err)
			}
			if err := a.Shutdown(2 * time.Second); err != nil {
				t.Fatalf("shutdown before restore: %v", err)
			}

			restored := newTestMainAgentForRestore(t, a.contentRoot, a.sessionDir)
			restored.SetAgentConfigs(a.agentConfigs)
			if err := restored.RestoreSessionAtStartup(); err != nil {
				t.Fatalf("restore session: %v", err)
			}
			record := restored.taskRecordByTaskID(sub.taskID)
			if record == nil || !record.RuntimeParked {
				t.Fatalf("restored task = %+v, want the parked worker", record)
			}
			restored.SwitchFocus(record.LatestInstanceID)
			restoredState := restored.FocusedModelState()
			if restoredState.DisplayRef != event.RunningModelRef || restored.focusedAgent.Load() != nil {
				t.Fatalf("restored parked display = %+v, want durable usage ref without a live worker", restoredState)
			}
			if got := restored.GetSidebarUsageStats().ByModel[restoredState.DisplayRef]; got == nil || *got != *model {
				t.Fatalf("restored sidebar sample = %+v, want live sample %+v", got, model)
			}
		})
	}
}
