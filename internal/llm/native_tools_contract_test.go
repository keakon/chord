package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func TestNativeSearchAdapterFreezesAuthorizationAndDeclaration(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: "https://example.invalid/v1/responses"}, nil)
	cfg := &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: provider.APIURL(), Preauthorized: true, AllowedDomains: []string{"example.invalid"}, MaxUses: 2}
	request, err := nativeWebSearchRequest(cfg, provider)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AllowedDomains[0] = "changed.invalid"
	cfg.MaxUses = 8
	body, err := addNativeToolDeclaration([]byte(`{"tools":[]}`), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "example.invalid") || strings.Contains(string(body), "changed.invalid") || !strings.Contains(string(body), `"max_tool_calls":2`) {
		t.Fatalf("declaration changed: %s", body)
	}
	auth := request.authorization
	if auth.Tool != toolname.WebSearch || auth.Contract != config.NativeWebSearchResponses || !strings.Contains(string(auth.Constraints), `"max_uses":2`) {
		t.Fatalf("authorization=%+v", auth)
	}
}

func TestNativeContinuationRechecksNamedPermission(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeAnthropicFixture("pause_turn"))
	}))
	defer srv.Close()
	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key"})
	impl, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(provider, impl, "test-model", 1024, "")
	defer client.Close()
	permitted := true
	var failed *message.NativeToolHistory
	policy := &NativeToolPolicy{
		Permitted: func(name string) bool {
			if name != toolname.WebSearch {
				t.Errorf("permission checked for %q", name)
			}
			return permitted
		},
		Begin: func(_ context.Context, r NativeRequestRecord) (string, error) {
			return fmt.Sprintf("request-%d", r.Continuation), nil
		},
		Finish: func(_ string, _ message.NativeRequestOutcome, _ *message.Response, _ error) error {
			permitted = false
			return nil
		},
		Failed: func(receipt *message.NativeToolHistory) { failed = receipt },
	}
	_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search sample records"}}, nil, nil, CompleteStreamOptions{NativeTools: policy})
	if !IsNativeToolError(err) || requests != 1 || failed == nil || !failed.OutcomeUnknown || failed.Authorization.Tool != toolname.WebSearch {
		t.Fatalf("requests=%d failure=%+v err=%v", requests, failed, err)
	}
}

func TestNativeAuthorizationIncludesToolAndContractIdentity(t *testing.T) {
	a := message.NativeToolAuthorization{Tool: "sample_tool", Contract: "sample.contract", Constraints: json.RawMessage(`{"limit":2}`)}
	b := a
	if !a.Equal(b) {
		t.Fatal("identical authorization changed")
	}
	b.Tool = "other_tool"
	if a.Equal(b) {
		t.Fatal("tool identity ignored")
	}
	b = a
	b.Contract = "other.contract"
	if a.Equal(b) {
		t.Fatal("contract identity ignored")
	}
	b = a
	b.Constraints = json.RawMessage(`{"limit":3}`)
	if a.Equal(b) {
		t.Fatal("constraints ignored")
	}
}
