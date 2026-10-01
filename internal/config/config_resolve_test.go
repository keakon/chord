package config

import (
	"strings"
	"testing"
)

func resolveTestConfig(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := loadConfigData("config.yaml", []byte(yaml), false, nil)
	if err != nil {
		t.Fatalf("loadConfigData: %v", err)
	}
	return cfg
}

func TestResolveConfiguredPoolRefs(t *testing.T) {
	valid := resolveTestConfig(t, `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          output: 20000
        variants:
          fast:
            reasoning:
              effort: high
model_pools:
  default:
    - sample-provider/model-1
    - sample-provider/model-1@fast
`)
	if diags := ResolveConfiguredPoolRefs(valid); len(diags) != 0 {
		t.Fatalf("ResolveConfiguredPoolRefs = %+v, want none for valid refs", diags)
	}

	broken := resolveTestConfig(t, `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          output: 20000
model_pools:
  default:
    - sample-provider/model-1
  secondary:
    - unknown-provider/model-1
    - sample-provider/gone
    - sample-provider/model-1@turbo
    - no-slash-ref
`)
	diags := ResolveConfiguredPoolRefs(broken)
	if len(diags) != 4 {
		t.Fatalf("ResolveConfiguredPoolRefs = %+v, want 4 broken refs", diags)
	}
	for _, d := range diags {
		if d.Severity != DiagnosticSeverityError {
			t.Fatalf("diagnostic %+v, want error severity", d)
		}
		if !d.Continues {
			t.Fatalf("diagnostic %+v, want continuing load", d)
		}
		if !strings.HasPrefix(d.Scope, "model pool secondary") {
			t.Fatalf("diagnostic scope %q, want the secondary pool", d.Scope)
		}
		if !strings.HasPrefix(d.Path, "model_pools.secondary") {
			t.Fatalf("diagnostic path %q, want the secondary pool path", d.Path)
		}
	}
	// Deterministic order: pool name order, then reference order.
	wantSubstrings := []string{"unknown-provider", "gone", "turbo", "provider/model"}
	for i, want := range wantSubstrings {
		if !strings.Contains(diags[i].Message, want) {
			t.Fatalf("diags[%d].Message = %q, want it to mention %q", i, diags[i].Message, want)
		}
	}
}

func TestResolveConfiguredPoolRefsEmptyPoolName(t *testing.T) {
	cfg := resolveTestConfig(t, `providers:
  sample-provider:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1:
        limit:
          context: 100000
          output: 20000
model_pools:
  "":
    - sample-provider/model-1
`)
	diags := ResolveConfiguredPoolRefs(cfg)
	if len(diags) != 1 || diags[0].Message != "model pool name must not be empty" {
		t.Fatalf("ResolveConfiguredPoolRefs = %+v, want the empty pool name diagnostic", diags)
	}
}

func TestResolveConfiguredPoolRefsNilAndEmpty(t *testing.T) {
	if diags := ResolveConfiguredPoolRefs(nil); diags != nil {
		t.Fatalf("ResolveConfiguredPoolRefs(nil) = %+v, want nil", diags)
	}
	cfg := resolveTestConfig(t, "providers: {}\n")
	if diags := ResolveConfiguredPoolRefs(cfg); diags != nil {
		t.Fatalf("ResolveConfiguredPoolRefs(no pools) = %+v, want nil", diags)
	}
}
