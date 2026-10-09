package config

import "testing"

func TestNativeSearchRequiresExactContractAndScope(t *testing.T) {
	base := NativeWebSearchConfig{Contract: NativeWebSearchResponses, APIURL: "https://example.invalid/v1/responses", AllowedDomains: []string{"example.invalid"}, MaxUses: 1}
	if err := base.Validate(ProviderTypeResponses, base.APIURL); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*NativeWebSearchConfig)
	}{
		{"endpoint", func(c *NativeWebSearchConfig) { c.APIURL += "/other" }},
		{"contract", func(c *NativeWebSearchConfig) { c.Contract = NativeWebSearchMessages }},
		{"budget", func(c *NativeWebSearchConfig) { c.MaxUses = 9 }},
		{"negative budget", func(c *NativeWebSearchConfig) { c.MaxUses = -1 }},
		{"filters", func(c *NativeWebSearchConfig) { c.BlockedDomains = []string{"example.org"} }},
		{"url", func(c *NativeWebSearchConfig) { c.AllowedDomains = []string{"https://example.invalid"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.change(&c)
			if err := c.Validate(ProviderTypeResponses, base.APIURL); err == nil {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
}
