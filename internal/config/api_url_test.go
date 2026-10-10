package config

import "testing"

func TestAPIURLPathHasSuffixIgnoresQueryAndFragment(t *testing.T) {
	cases := []struct {
		name   string
		apiURL string
		suffix string
		want   bool
	}{
		{
			name:   "responses query",
			apiURL: "https://example.invalid/v1/responses?api-version=v1",
			suffix: "/responses",
			want:   true,
		},
		{
			name:   "messages fragment",
			apiURL: "https://example.invalid/v1/messages#debug",
			suffix: "/messages",
			want:   true,
		},
		{
			name:   "models query and trailing slash",
			apiURL: "https://example.invalid/v1beta/models/?foo=bar",
			suffix: "/models",
			want:   true,
		},
		{
			name:   "non matching path",
			apiURL: "https://example.invalid/v1/responses-extra?api-version=v1",
			suffix: "/responses",
			want:   false,
		},
		{
			name:   "empty url",
			apiURL: "   ",
			suffix: "/responses",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := APIURLPathHasSuffix(tc.apiURL, tc.suffix); got != tc.want {
				t.Fatalf("APIURLPathHasSuffix(%q, %q) = %v, want %v", tc.apiURL, tc.suffix, got, tc.want)
			}
		})
	}
}

func TestInferProviderTypeFromAPIURL_GeminiRoots(t *testing.T) {
	if got := InferProviderTypeFromAPIURL("https://generativelanguage.googleapis.com/v1beta"); got != ProviderTypeGenerateContent {
		t.Fatalf("InferProviderTypeFromAPIURL(gemini) = %q", got)
	}
	if got := InferProviderTypeFromAPIURL("https://generativelanguage.googleapis.com/v1beta/"); got != ProviderTypeGenerateContent {
		t.Fatalf("InferProviderTypeFromAPIURL(gemini trailing slash) = %q", got)
	}
}

func TestInferProviderTypeFromAPIURLIgnoresQuery(t *testing.T) {
	cases := []struct {
		name   string
		apiURL string
		want   string
	}{
		{"responses", "https://example.invalid/openai/v1/responses?api-version=v1", ProviderTypeResponses},
		{"messages", "https://example.invalid/v1/messages?version=preview", ProviderTypeMessages},
		{"chat completions", "https://example.invalid/v1/chat/completions?source=test", ProviderTypeChatCompletions},
		{"Gemini root", "https://example.invalid/v1beta/?region=test", ProviderTypeGenerateContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InferProviderTypeFromAPIURL(tc.apiURL); got != tc.want {
				t.Fatalf("InferProviderTypeFromAPIURL(%q) = %q, want %q", tc.apiURL, got, tc.want)
			}
		})
	}
}

func TestEffectiveProviderType(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProviderConfig
		want string
	}{
		{"explicit type wins", ProviderConfig{Type: ProviderTypeMessages, APIURL: "https://example.invalid/v1/responses"}, ProviderTypeMessages},
		{"codex preset", ProviderConfig{Preset: ProviderPresetCodex}, ProviderTypeResponses},
		{"inferred from api_url", ProviderConfig{APIURL: "https://example.invalid/v1/chat/completions"}, ProviderTypeChatCompletions},
		{"unresolvable", ProviderConfig{APIURL: "https://example.invalid/v1"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveProviderType(tc.cfg); got != tc.want {
				t.Fatalf("EffectiveProviderType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGeminiVersionRootDetection(t *testing.T) {
	for _, tc := range []struct {
		url             string
		valid, inferred bool
	}{
		{"https://example.invalid/proxy/v1beta/?region=test#fragment", true, true},
		{"https://example.invalid/v1alpha", true, true},
		{"https://generativelanguage.googleapis.com/v1/", true, true},
		{"https://example.invalid/v1/", true, false},
		{"https://example.invalid/v1beta/models", false, false},
		{"https://example.invalid/v1beta/openai/chat/completions", false, false},
		{"ftp://example.invalid/v1beta", false, false},
		{"https://key@example.invalid/v1beta", false, false},
	} {
		if got := IsGeminiAPIURL(tc.url); got != tc.valid {
			t.Errorf("IsGeminiAPIURL(%q) = %v, want %v", tc.url, got, tc.valid)
		}
		if got := InferProviderTypeFromAPIURL(tc.url) == ProviderTypeGenerateContent; got != tc.inferred {
			t.Errorf("Gemini inference for %q = %v, want %v", tc.url, got, tc.inferred)
		}
	}
}
