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
