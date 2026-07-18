package permission

import "testing"

func mustWebFetchTarget(t *testing.T, rawURL string) webFetchTarget {
	t.Helper()
	target, ok := parseWebFetchTarget(rawURL)
	if !ok {
		t.Fatalf("parseWebFetchTarget(%q) failed", rawURL)
	}
	return target
}

func TestMatchWebFetchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		url     string
		want    bool
	}{
		// wildcard
		{"*", "http://example.com/", true},
		// CIDR v4
		{"0.0.0.0/8", "http://0.1.2.3/", true},
		{"0.0.0.0/8", "http://1.2.3.4/", false},
		{"10.0.0.0/8", "http://10.255.1.2/", true},
		{"169.254.0.0/16", "http://169.254.169.254/latest/meta-data/", true},
		{"192.168.0.0/16", "http://192.168.50.1/", true},
		{"10.0.0.0/8", "http://11.0.0.1/", false},
		// CIDR v4 with port
		{"10.0.0.0/8:443", "https://10.1.2.3/", true},
		{"10.0.0.0/8:443", "http://10.1.2.3/", false},
		{"10.0.0.0/8:8000-9000", "http://10.1.2.3:8500/", true},
		{"10.0.0.0/8:8000-9000", "http://10.1.2.3:9001/", false},
		// literal IP and host
		{"127.0.0.1", "http://127.0.0.1:8080/", true},
		{"127.0.0.1", "http://127.0.0.2/", false},
		{"example.com", "http://example.com/anything", true},
		{"example.com", "http://example.com./anything", true},
		{"example.com", "http://example.com.evil.com/", false},
		{"*.internal", "https://api.internal/", true},
		{"*.internal", "https://api.internal./", true},
		{"*.internal", "https://internal/", false},
		{"api-*.example.com", "https://api-prod.example.com/", true},
		{"api-????.example.com", "https://api-prod.example.com/", true},
		{"api-*.example.com", "https://web-prod.example.com/", false},
		{"xn--bcher-kva.example", "https://bücher.example/", true},
		{"*.bücher.example", "https://api.xn--bcher-kva.example/", true},
		{"*.xn--bcher-kva.example", "https://api.bücher.example/", true},
		// port and default port inference
		{"*:8000-9000", "http://a.com:8500/", true},
		{"*:8000-9000", "http://a.com:80/", false},
		{"example.com:443", "https://example.com/", true},
		{"example.com:443", "http://example.com/", false},
		{"*:0", "http://example.com:80/", false},
		{"*:65536", "http://example.com:80/", false},
		{"*:1-65535", "http://example.com:443/", true},
		{"*:0-65535", "http://example.com:443/", false},
		// IPv6 and IPv6 CIDR with optional port
		{"fd00::/8", "http://[fd00::1]/", true},
		{"fd00::/8", "http://[fe80::1]/", false},
		{"::1", "http://[::1]/", true},
		{"[::1]:8080", "http://[::1]:8080/", true},
		{"[::1]:8080", "http://[::1]:9090/", false},
		{"[fd00::/8]:443", "https://[fd00::1]/", true},
		{"[fd00::/8]:443", "http://[fd00::1]/", false},
		// CIDR patterns never resolve domain targets.
		{"127.0.0.0/8", "http://localhost/", false},
		// Scheme and path syntax are unsupported.
		{"https://example.com", "https://example.com/page", false},
		{"localhost:8000/*", "http://localhost:8000/foo", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"__"+tc.url, func(t *testing.T) {
			got := matchWebFetchPattern(tc.pattern, mustWebFetchTarget(t, tc.url))
			if got != tc.want {
				t.Fatalf("matchWebFetchPattern(%q, %q) = %v, want %v", tc.pattern, tc.url, got, tc.want)
			}
		})
	}
}

func TestParseWebFetchTargetRejectsInvalidPorts(t *testing.T) {
	for _, rawURL := range []string{
		"http://example.com:0/",
		"http://example.com:65536/",
	} {
		if target, ok := parseWebFetchTarget(rawURL); ok {
			t.Fatalf("parseWebFetchTarget(%q) = %+v, want rejection", rawURL, target)
		}
	}
}

func exampleWebFetchRuleset() Ruleset {
	return Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "web_fetch", Pattern: "0.0.0.0/8", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "10.0.0.0/8", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "127.0.0.0/8", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "169.254.0.0/16", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "192.168.0.0/16", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "*.internal", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "fd00::/8", Action: ActionDeny},
	}
}

func TestEvaluateWebFetch(t *testing.T) {
	rs := exampleWebFetchRuleset()
	cases := []struct {
		name string
		url  string
		want Action
	}{
		{"public host allowed", "https://example.com/", ActionAllow},
		{"loopback cidr denied", "http://127.5.5.5/x", ActionDeny},
		{"metadata denied", "http://169.254.169.254/latest/meta-data/", ActionDeny},
		{"private 10 denied", "http://10.1.2.3/x", ActionDeny},
		{"private 192.168 denied", "http://192.168.1.1/", ActionDeny},
		{"cgnat not in ranges allowed", "http://100.64.0.1/", ActionAllow},
		{"internal domain https denied", "https://api.internal/", ActionDeny},
		{"internal domain http denied", "http://api.internal/", ActionDeny},
		{"ipv6 ula denied", "http://[fd00::1]/", ActionDeny},
		{"ipv6 loopback no rule allowed", "http://[::1]/", ActionAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rs.EvaluateWebFetch(tc.url)
			if !got.Found || got.Rule.Action != tc.want {
				t.Fatalf("EvaluateWebFetch(%q) = %+v, want action %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestEvaluateWebFetchLastMatchWins(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "web_fetch", Pattern: "127.0.0.0/8", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "*:8000-9000", Action: ActionAsk},
	}
	if got := rs.EvaluateWebFetch("http://127.0.0.1:8080/"); got.Rule.Action != ActionAsk {
		t.Fatalf("port-range ask should override cidr deny, got %v", got.Rule.Action)
	}
	if got := rs.EvaluateWebFetch("http://127.0.0.1/"); got.Rule.Action != ActionDeny {
		t.Fatalf("cidr deny should apply off the port range, got %v", got.Rule.Action)
	}
}

func TestEvaluateWebFetchUnsupportedPatternsDoNotMatch(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "web_fetch", Pattern: "https://example.com", Action: ActionDeny},
		{Permission: "web_fetch", Pattern: "example.com/private/*", Action: ActionDeny},
	}
	for _, rawURL := range []string{"https://example.com/", "https://example.com/private/data"} {
		if got := rs.EvaluateWebFetch(rawURL); !got.Found || got.Rule.Action != ActionAllow {
			t.Fatalf("EvaluateWebFetch(%q) = %+v, want wildcard allow", rawURL, got)
		}
	}
}

func TestEvaluateWebFetchUnparseableURLUsesWildcardOnly(t *testing.T) {
	rs := Ruleset{
		{Permission: "web_fetch", Pattern: "*", Action: ActionAllow},
		{Permission: "web_fetch", Pattern: "not a url*", Action: ActionDeny},
	}
	if got := rs.EvaluateWebFetch("not a url here"); !got.Found || got.Rule.Action != ActionAllow {
		t.Fatalf("expected wildcard allow, got %+v", got)
	}
}
