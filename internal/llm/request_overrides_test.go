package llm

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestApplyRequestBodyOverrides(t *testing.T) {
	maxTokens := "max_tokens"
	body, err := applyRequestBodyOverrides([]byte(`{"max_completion_tokens":64,"reasoning":{"effort":"high","summary":"auto"},"stream":true}`), config.RequestOverridesConfig{
		RenameBodyFields: map[string]*string{"max_completion_tokens": &maxTokens},
		Body: map[string]any{
			"thinking":  map[string]any{"type": "enabled"},
			"reasoning": map[string]any{"effort": "max", "summary": nil},
			"stream":    nil,
		},
	})
	if err != nil {
		t.Fatalf("applyRequestBodyOverrides: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal patched body: %v", err)
	}
	if got["max_tokens"] != float64(64) {
		t.Fatalf("max_tokens = %#v, want 64", got["max_tokens"])
	}
	if _, ok := got["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens should be renamed")
	}
	if _, ok := got["stream"]; ok {
		t.Fatal("stream should be deleted by null override")
	}
	reasoning := got["reasoning"].(map[string]any)
	if reasoning["effort"] != "max" {
		t.Fatalf("reasoning.effort = %#v, want max", reasoning["effort"])
	}
	if _, ok := reasoning["summary"]; ok {
		t.Fatal("reasoning.summary should be deleted by nested null")
	}
	if got["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("thinking = %#v", got["thinking"])
	}
}

func TestApplyRequestBodyOverridesRenamesFromOriginalRequest(t *testing.T) {
	aTarget := "b"
	bTarget := "c"
	body, err := applyRequestBodyOverrides([]byte(`{"a":9007199254740993,"b":2}`), config.RequestOverridesConfig{
		RenameBodyFields: map[string]*string{"a": &aTarget, "b": &bTarget},
	})
	if err != nil {
		t.Fatalf("applyRequestBodyOverrides: %v", err)
	}
	if got, want := string(body), `{"b":9007199254740993,"c":2}`; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestApplyRequestBodyOverridesRejectsDuplicateRenameTargets(t *testing.T) {
	target := "renamed"
	_, err := applyRequestBodyOverrides([]byte(`{"a":1,"b":2}`), config.RequestOverridesConfig{
		RenameBodyFields: map[string]*string{"a": &target, "b": &target},
	})
	if err == nil {
		t.Fatal("expected duplicate rename target error")
	}
}

func TestApplyRequestHeaderOverrides(t *testing.T) {
	header := http.Header{"Anthropic-Beta": []string{"legacy"}, "X-Keep": []string{"yes"}}
	trace := "model"
	applyRequestHeaderOverrides(header, config.RequestOverridesConfig{Headers: map[string]*string{
		"anthropic-beta": nil,
		"x-trace":        &trace,
	}})
	if got := header.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta = %q, want removed", got)
	}
	if got := header.Get("x-trace"); got != "model" {
		t.Fatalf("x-trace = %q, want model", got)
	}
	if got := header.Get("x-keep"); got != "yes" {
		t.Fatalf("x-keep = %q, want preserved", got)
	}
}
