package llm

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestConnectLadderKeepsServerResponseAcrossKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testProviderConfigWithKeys("primary", "test-model", []string{"key-a", "key-b"})
		cfg.retryBackoff = config.RetryBackoffNone
		impl := &recordingProvider{}
		for range 2 {
			impl.calls = append(impl.calls,
				scriptedCall{err: &APIError{StatusCode: 500, Message: "upstream overloaded"}},
				scriptedCall{err: unresolvedHostErr()},
			)
		}
		impl.calls = append(impl.calls, scriptedCall{resp: &message.Response{Content: "recovered"}})
		client := NewClient(cfg, impl, "test-model", 4096, "")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var deadlines []time.Time
		resp, err := callCompleteStreamWithRetryForTest(client, ctx, cfg, impl,
			"test-model", 4096, RequestTuning{}, "", []message.Message{{Role: "user", Content: "hello"}},
			nil, func(d message.StreamDelta) {
				if d.Status != nil && d.Status.Type == message.StatusDeltaRetrying && strings.HasPrefix(d.Status.Detail, "round ") {
					deadlines = append(deadlines, d.Status.Deadline)
				}
			}, false, nil, -3, &CallStatus{})
		if err != nil || resp == nil || resp.Content != "recovered" || impl.CallCount() != 5 {
			t.Fatalf("response=%#v calls=%d err=%v", resp, impl.CallCount(), err)
		}
		if len(deadlines) != 2 {
			t.Fatalf("round deadlines=%v, want two retries", deadlines)
		}
		for _, deadline := range deadlines {
			if !deadline.IsZero() {
				t.Fatalf("server response lost during key rotation: deadlines=%v", deadlines)
			}
		}
	})
}

func TestConnectLadderDistinguishesLocalAndServerContextRejection(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "server"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				primary := testProviderConfig("primary", "test-model")
				primary.retryBackoff = config.RetryBackoffNone
				primaryImpl := &recordingProvider{}
				if local {
					model := primary.models["test-model"]
					model.Limit.Context = 1
					primary.models["test-model"] = model
				} else {
					primaryImpl.calls = []scriptedCall{
						{err: &APIError{StatusCode: 413, Message: "context too long"}},
						{err: &APIError{StatusCode: 413, Message: "context too long"}},
					}
				}
				sibling := testProviderConfig("sibling", "test-model")
				sibling.retryBackoff = config.RetryBackoffNone
				siblingImpl := &recordingProvider{}
				siblingImpl.calls = []scriptedCall{
					{err: unresolvedHostErr()},
					{resp: &message.Response{Content: "recovered"}},
				}
				client := NewClient(primary, primaryImpl, "test-model", 4096, "")
				if local {
					client.setLastInputTokens(1)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				var retry *message.StatusDelta
				resp, err := callCompleteStreamWithRetryForTest(client, ctx, primary, primaryImpl,
					"test-model", 4096, RequestTuning{}, "", []message.Message{{Role: "user", Content: "hello"}},
					nil, func(d message.StreamDelta) {
						if d.Status != nil && d.Status.Detail == "round 2" {
							retry = d.Status
						}
					}, true, []FallbackModel{{ProviderConfig: sibling, ProviderImpl: siblingImpl, ModelID: "test-model", MaxTokens: 4096}},
					-2, &CallStatus{})
				if err != nil || resp == nil || resp.Content != "recovered" || retry == nil {
					t.Fatalf("response=%#v retry=%#v err=%v", resp, retry, err)
				}
				wantCalls := 2
				if local {
					wantCalls = 0
				}
				if primaryImpl.CallCount() != wantCalls || siblingImpl.CallCount() != 2 {
					t.Fatalf("provider calls=%d/%d, want %d/2", primaryImpl.CallCount(), siblingImpl.CallCount(), wantCalls)
				}
				if retry.Deadline.IsZero() == local {
					t.Fatalf("local=%v retry deadline=%v", local, retry.Deadline)
				}
			})
		})
	}
}

func TestConnectLadderKeepsResponseHeadersBeforeReconnectFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testProviderConfig("primary", "test-model")
		cfg.retryBackoff = config.RetryBackoffNone
		impl := &recordingProvider{}
		impl.calls = []scriptedCall{
			{
				streams: []message.StreamDelta{
					{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingHeaders}},
					{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaConnecting}},
				},
				err: unresolvedHostErr(),
			},
			{resp: &message.Response{Content: "recovered"}},
		}
		client := NewClient(cfg, impl, "test-model", 4096, "")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var retry *message.StatusDelta
		resp, err := callCompleteStreamWithRetryForTest(client, ctx, cfg, impl,
			"test-model", 4096, RequestTuning{}, "", []message.Message{{Role: "user", Content: "hello"}},
			nil, func(d message.StreamDelta) {
				if d.Status != nil && d.Status.Detail == "round 2" {
					retry = d.Status
				}
			}, false, nil, -2, &CallStatus{})
		if err != nil || resp == nil || resp.Content != "recovered" || retry == nil {
			t.Fatalf("response=%#v retry=%#v err=%v", resp, retry, err)
		}
		if !retry.Deadline.IsZero() {
			t.Fatalf("response headers lost after reconnect failure: deadline=%v", retry.Deadline)
		}
	})
}
