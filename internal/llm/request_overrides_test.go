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

func TestWithoutReasoningRequestOverrides(t *testing.T) {
	maxTokens := "max_tokens"
	thinking := "thinking"
	overrides := withoutReasoningRequestOverrides(config.RequestOverridesConfig{
		Body: map[string]any{
			"thinking":         map[string]any{"type": "enabled"},
			"reasoning":        map[string]any{"effort": "high"},
			"reasoning_effort": "high",
			"keep":             true,
		},
		RenameBodyFields: map[string]*string{
			"max_completion_tokens": &maxTokens,
			"reasoning_effort":      &thinking,
		},
	})
	for _, key := range []string{"thinking", "reasoning", "reasoning_effort"} {
		if _, ok := overrides.Body[key]; ok {
			t.Fatalf("body override %q was not removed: %#v", key, overrides.Body)
		}
	}
	if overrides.Body["keep"] != true || overrides.RenameBodyFields["max_completion_tokens"] == nil {
		t.Fatalf("unrelated overrides were not preserved: %#v", overrides)
	}
	if _, ok := overrides.RenameBodyFields["reasoning_effort"]; ok {
		t.Fatalf("reasoning rename was not removed: %#v", overrides.RenameBodyFields)
	}
}

func TestRequestOverridesEnableReasoning(t *testing.T) {
	tests := []struct {
		name      string
		overrides config.RequestOverridesConfig
		want      bool
	}{
		{
			name: "thinking enabled object",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			}},
			want: true,
		},
		{
			name: "reasoning effort high",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"reasoning": map[string]any{"effort": "high"},
			}},
			want: true,
		},
		{
			name: "reasoning effort top level",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"reasoning_effort": "max",
			}},
			want: true,
		},
		{
			name: "thinking disabled object",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"thinking": map[string]any{"type": "disabled"},
			}},
			want: false,
		},
		{
			name: "reasoning none object",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"reasoning": map[string]any{"effort": "none"},
			}},
			want: false,
		},
		{
			name: "reasoning effort none",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"reasoning_effort": "none",
			}},
			want: false,
		},
		{
			name: "reasoning summary none",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"reasoning": map[string]any{"summary": "none"},
			}},
			want: false,
		},
		{
			name: "bool false",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"thinking": false,
			}},
			want: false,
		},
		{
			name: "unrelated override",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"stream": false,
			}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestOverridesEnableReasoning(tt.overrides); got != tt.want {
				t.Fatalf("requestOverridesEnableReasoning() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveRequestReasoningActiveHonorsFinalOverride(t *testing.T) {
	tests := []struct {
		name      string
		base      map[string]any
		overrides config.RequestOverridesConfig
		want      bool
	}{
		{
			name: "override disables generated effort",
			base: map[string]any{"reasoning_effort": "high"},
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"reasoning_effort": "none",
			}},
			want: false,
		},
		{
			name: "override enables reasoning without tuning",
			overrides: config.RequestOverridesConfig{Body: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			}},
			want: true,
		},
		{
			name: "renamed generated effort remains visible",
			base: map[string]any{"reasoning_effort": "high"},
			overrides: config.RequestOverridesConfig{RenameBodyFields: map[string]*string{
				"reasoning_effort": new("thinking"),
			}},
			want: true,
		},
		{
			name: "custom renamed generated effort remains active",
			base: map[string]any{"reasoning_effort": "high"},
			overrides: config.RequestOverridesConfig{RenameBodyFields: map[string]*string{
				"reasoning_effort": new("reasoningEffort"),
			}},
			want: true,
		},
		{
			name: "custom renamed generated effort can be disabled",
			base: map[string]any{"reasoning_effort": "high"},
			overrides: config.RequestOverridesConfig{
				RenameBodyFields: map[string]*string{"reasoning_effort": new("reasoningEffort")},
				Body:             map[string]any{"reasoningEffort": "none"},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := effectiveRequestReasoningActive(tt.base, tt.overrides)
			if err != nil {
				t.Fatalf("effectiveRequestReasoningActive: %v", err)
			}
			if got != tt.want {
				t.Fatalf("effectiveRequestReasoningActive() = %v, want %v", got, tt.want)
			}
		})
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
