package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestHostedHeadersProtectTransportAndCredentials(t *testing.T) {
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "X-Api-Key", "Api-Key", "Host",
		"Content-Type", "Accept", "Content-Encoding", "Accept-Encoding",
		"Content-Length", "Transfer-Encoding", "Anthropic-Version", "ChatGPT-Account-ID",
		"Session_Id", "Session-Id", "X-Session-Id", "X-Codex-Turn-State",
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{"Authorization": {"Bearer original"}}
			err := applyHostedToolHeaders(header, &HostedToolRequest{Headers: map[string]string{strings.ToLower(name): "override"}})
			if err == nil || header.Get("Authorization") != "Bearer original" {
				t.Fatalf("err = %v, headers = %#v", err, header)
			}
		})
	}
	for _, headers := range []map[string]string{
		{"bad name": "sample"}, {"x-sample": "bad\r\nvalue"},
		{"x-sample": "one", "X-Sample": "two"},
	} {
		if err := applyHostedToolHeaders(make(http.Header), &HostedToolRequest{Headers: headers}); err == nil {
			t.Fatalf("invalid headers accepted: %#v", headers)
		}
	}
	header := http.Header{"Anthropic-Beta": {"provider-default"}}
	if err := applyHostedToolHeaders(header, &HostedToolRequest{Headers: map[string]string{"anthropic-beta": "sample-beta", "x-sample": "enabled"}}); err != nil {
		t.Fatal(err)
	}
	if header.Get("Anthropic-Beta") != "sample-beta" || header.Get("X-Sample") != "enabled" {
		t.Fatalf("headers = %#v", header)
	}
}

func TestHostedTransportsRejectProtectedHeadersBeforeSending(t *testing.T) {
	for _, family := range []string{config.ProviderTypeMessages, config.ProviderTypeResponses} {
		t.Run(family, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer srv.Close()
			pc := NewProviderConfig("sample", config.ProviderConfig{Type: family, APIURL: srv.URL}, []string{"test-key"})
			var impl Provider
			var err error
			declaration := sampleResponsesDeclaration
			if family == config.ProviderTypeMessages {
				impl, err = NewAnthropicProvider(pc, "")
				declaration = sampleAnthropicDeclaration
			} else {
				impl, err = NewResponsesProvider(pc, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = impl.CompleteStream(context.Background(), "test-key", "test-model", "system",
				[]message.Message{{Role: message.RoleUser, Content: "sample query"}}, nil, 1024,
				RequestTuning{HostedTool: &HostedToolRequest{
					Name: "sample_tool", Declaration: json.RawMessage(declaration), Headers: map[string]string{"Authorization": "override"},
				}}, func(message.StreamDelta) {})
			if err == nil || requests.Load() != 0 {
				t.Fatalf("err = %v, sent requests = %d", err, requests.Load())
			}
		})
	}
}
