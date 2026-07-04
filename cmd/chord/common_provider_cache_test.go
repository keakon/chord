package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

func TestOAuthCredentialMapIncludesRefreshStateForAccessCredential(t *testing.T) {
	access := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"user_id":"user-1","chatgpt_account_id":"acc-1"}}`)) + ".sig"
	creds := []config.ProviderCredential{{OAuth: &config.OAuthCredential{Access: access, Refresh: "refresh-token", AccountID: "acc-1"}}}
	m, _, err := oauthCredentialMapWithOptions(creds, false)
	if err != nil {
		t.Fatalf("oauthCredentialMapWithOptions: %v", err)
	}
	setup := m[llm.OAuthKeySetupSlotKey(0, access)]
	if setup.RefreshSHA256 != config.OAuthRefreshStateKey("refresh-token") {
		t.Fatalf("RefreshSHA256 = %q, want refresh token state key", setup.RefreshSHA256)
	}
}

func TestNormalizeProviderConfigDetectsGeminiFromModelsPath(t *testing.T) {
	got, err := normalizeProviderConfig("gemini", config.ProviderConfig{
		APIURL: "https://generativelanguage.googleapis.com/v1beta/models",
		Models: map[string]config.ModelConfig{"gemini-2.5-flash": {}},
	}, nil)
	if err != nil {
		t.Fatalf("normalizeProviderConfig() error = %v", err)
	}
	if got.Type != config.ProviderTypeGenerateContent {
		t.Fatalf("Type = %q, want %q", got.Type, config.ProviderTypeGenerateContent)
	}
	if got.APIURL != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Fatalf("APIURL = %q", got.APIURL)
	}
}

func TestNormalizeProviderConfigDetectsTypeFromURLPathWithQuery(t *testing.T) {
	cases := []struct {
		name   string
		apiURL string
		want   string
	}{
		{
			name:   "responses",
			apiURL: "https://example.invalid/openai/v1/responses?api-version=v1",
			want:   config.ProviderTypeResponses,
		},
		{
			name:   "messages",
			apiURL: "https://example.invalid/v1/messages?version=preview",
			want:   config.ProviderTypeMessages,
		},
		{
			name:   "chat completions",
			apiURL: "https://example.invalid/v1/chat/completions?source=test",
			want:   config.ProviderTypeChatCompletions,
		},
		{
			name:   "generate content",
			apiURL: "https://example.invalid/v1beta/models?region=test",
			want:   config.ProviderTypeGenerateContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeProviderConfig("sample", config.ProviderConfig{
				APIURL: tc.apiURL,
				Models: map[string]config.ModelConfig{"test-model": {}},
			}, nil)
			if err != nil {
				t.Fatalf("normalizeProviderConfig() error = %v", err)
			}
			if got.Type != tc.want {
				t.Fatalf("Type = %q, want %q", got.Type, tc.want)
			}
		})
	}
}

func TestNormalizeProviderConfigGeminiRequiresAPIURL(t *testing.T) {
	_, err := normalizeProviderConfig("gemini", config.ProviderConfig{
		Type:   config.ProviderTypeGenerateContent,
		Models: map[string]config.ModelConfig{"gemini-2.5-flash": {}},
	}, nil)
	if err == nil {
		t.Fatal("normalizeProviderConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "requires api_url") {
		t.Fatalf("error = %v, want requires api_url", err)
	}
}

func TestNormalizeProviderConfigAzurePresetSetsResponsesDefaults(t *testing.T) {
	got, err := normalizeProviderConfig("azure", config.ProviderConfig{
		Preset: config.ProviderPresetAzure,
		APIURL: "https://example.openai.azure.com/openai/v1/responses?api-version=preview",
		Models: map[string]config.ModelConfig{"gpt-5.5": {}},
	}, nil)
	if err != nil {
		t.Fatalf("normalizeProviderConfig() error = %v", err)
	}
	if got.Type != config.ProviderTypeResponses {
		t.Fatalf("Type = %q, want %q", got.Type, config.ProviderTypeResponses)
	}
	if got.Store == nil || *got.Store != true {
		t.Fatalf("Store = %v, want true", got.Store)
	}
}
