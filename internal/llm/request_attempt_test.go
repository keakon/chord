package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestPrepareAttemptBudgetSkipsTargetAndPreservesPool(t *testing.T) {
	primaryConfig := testProviderConfig("sample-primary", "small")
	fallbackConfig := testProviderConfig("sample-fallback", "large")
	primary := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "unexpected", StopReason: "stop"}}}}
	fallback := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "accepted", StopReason: "stop"}}}}
	client := NewClient(primaryConfig, primary, "small", 1024, "rules")
	client.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackConfig, ProviderImpl: fallback, ModelID: "large", MaxTokens: 1024}})
	var attempts []string
	response, err := client.CompleteStreamWithOptions(context.Background(), []message.Message{{Role: message.RoleUser, Content: "request"}}, nil, nil, CompleteStreamOptions{PrepareAttempt: func(ctx context.Context, a RequestAttempt) (context.Context, error) {
		attempts = append(attempts, a.Model.ModelID)
		if a.SystemPrompt != "rules" || len(a.Messages) == 0 {
			t.Fatal("incomplete attempt surface")
		}
		if a.Model.ModelID == "small" {
			return ctx, &RequestInputBudgetError{ModelRef: "sample-primary/small", EstimatedTokens: 4096, InputBudget: 1024}
		}
		return ctx, nil
	}})
	if err != nil || response == nil || response.Content != "accepted" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if len(attempts) != 2 || attempts[0] != "small" || attempts[1] != "large" {
		t.Fatalf("attempts=%v", attempts)
	}
}
func TestPrepareAttemptInvariantErrorStopsBeforeProvider(t *testing.T) {
	cfg := testProviderConfig("sample", "model")
	provider := &scriptedProvider{}
	client := NewClient(cfg, provider, "model", 1024, "")
	want := errors.New("missing rule body")
	_, err := client.CompleteStreamWithOptions(context.Background(), []message.Message{{Role: message.RoleUser, Content: "request"}}, nil, nil, CompleteStreamOptions{PrepareAttempt: func(ctx context.Context, _ RequestAttempt) (context.Context, error) { return ctx, want }})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	if !IsContextLengthExceeded(&RequestInputBudgetError{}) {
		t.Fatal("budget error not classified")
	}
}
