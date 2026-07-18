package agent

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func TestEvaluateToolPermissionWebFetch(t *testing.T) {
	node := parsePermissionNode(t, `
"*": allow
web_fetch:
  "169.254.0.0/16": deny
  "10.0.0.0/8": deny
  "*.internal": deny
  "*:8000-9000": ask
`)
	ruleset := permission.ParsePermission(&node)

	cases := []struct {
		url  string
		want permission.Action
	}{
		{"https://example.com/", permission.ActionAllow},
		{"http://169.254.169.254/latest/meta-data/", permission.ActionDeny},
		{"http://10.1.2.3/x", permission.ActionDeny},
		{"https://api.internal/", permission.ActionDeny},
		{"http://public.example:8500/", permission.ActionAsk},
		{"http://public.example:7000/", permission.ActionAllow},
	}
	for _, tc := range cases {
		got := evaluateToolPermission(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, tc.url))
		if got.Action != tc.want {
			t.Fatalf("web_fetch %q action = %q, want %q", tc.url, got.Action, tc.want)
		}
		if got.MatchArgument != tc.url {
			t.Fatalf("web_fetch %q match argument = %q, want %q", tc.url, got.MatchArgument, tc.url)
		}
	}
}

func TestEvaluateToolPermissionWebFetchUnsupportedSchemeAndPathRules(t *testing.T) {
	node := parsePermissionNode(t, `
"*": allow
web_fetch:
  "http://localhost:8000": ask
  "localhost:8000/*": deny
`)
	ruleset := permission.ParsePermission(&node)

	if got := evaluateToolPermission(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, "http://localhost:8000/admin")); got.Action != permission.ActionAllow {
		t.Fatalf("unsupported path rule should not match, got %q", got.Action)
	}
	if got := evaluateToolPermission(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, "http://localhost:9000/admin")); got.Action != permission.ActionAllow {
		t.Fatalf("non-matching path glob should allow, got %q", got.Action)
	}
}
