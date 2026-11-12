package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
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
		got := evaluateToolPermissionInDir(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, tc.url), permission.PathScope{})
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

	if got := evaluateToolPermissionInDir(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, "http://localhost:8000/admin"), permission.PathScope{}); got.Action != permission.ActionAllow {
		t.Fatalf("unsupported path rule should not match, got %q", got.Action)
	}
	if got := evaluateToolPermissionInDir(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, "http://localhost:9000/admin"), permission.PathScope{}); got.Action != permission.ActionAllow {
		t.Fatalf("non-matching path glob should allow, got %q", got.Action)
	}
}

func TestWithWebFetchAuthorizer(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
web_fetch:
  "example.com": allow
  "ask.example": ask
  "blocked.example": deny
`)
	ruleset := permission.ParsePermission(&node)
	pipeline := toolExecutionPipeline{currentRuleset: func() permission.Ruleset { return ruleset }}

	authorize := tools.WebFetchAuthorizerFromContext(pipeline.withWebFetchAuthorizer(context.Background()))
	if authorize == nil {
		t.Fatalf("expected authorizer to be installed for a non-empty ruleset")
	}
	if err := authorize("https://example.com/next"); err != nil {
		t.Fatalf("allowed host rejected: %v", err)
	}
	if err := authorize("https://other.example/next"); err == nil {
		t.Fatalf("unmatched host should be rejected")
	}
	if err := authorize("https://ask.example/next"); err == nil {
		t.Fatalf("ask host should be rejected without a YOLO relax")
	}

	// SubAgent inheritance: the ask relax follows the parent's YOLO mode while
	// a deny host stays rejected.
	sub := pipeline
	sub.yoloDowngradeAsk = func(string) bool { return true }
	authorize = tools.WebFetchAuthorizerFromContext(sub.withWebFetchAuthorizer(context.Background()))
	if err := authorize("https://ask.example/next"); err != nil {
		t.Fatalf("ask host should follow the inherited YOLO relax, got %v", err)
	}
	if err := authorize("https://blocked.example/next"); err == nil {
		t.Fatalf("deny should stay rejected under the inherited YOLO relax")
	}

	// Main agent: the YOLO transformation lives in the evaluated ruleset, so
	// the ask host follows without any downgrade hook.
	transformed := toolExecutionPipeline{currentRuleset: func() permission.Ruleset { return ruleset.RelaxCalls() }}
	authorize = tools.WebFetchAuthorizerFromContext(transformed.withWebFetchAuthorizer(context.Background()))
	if err := authorize("https://ask.example/next"); err != nil {
		t.Fatalf("ask host should follow the YOLO transformation, got %v", err)
	}
	if err := authorize("https://blocked.example/next"); err == nil {
		t.Fatalf("deny should stay rejected under the YOLO transformation")
	}

	// An empty ruleset installs nothing, matching the skipped pre-fetch check.
	empty := toolExecutionPipeline{currentRuleset: func() permission.Ruleset { return permission.Ruleset{} }}
	if got := tools.WebFetchAuthorizerFromContext(empty.withWebFetchAuthorizer(context.Background())); got != nil {
		t.Fatalf("empty ruleset should not install an authorizer")
	}
	// No ruleset source installs nothing.
	if got := tools.WebFetchAuthorizerFromContext((toolExecutionPipeline{}).withWebFetchAuthorizer(context.Background())); got != nil {
		t.Fatalf("missing ruleset should not install an authorizer")
	}
}
