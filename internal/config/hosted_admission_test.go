package config

import (
	"path/filepath"
	"testing"
)

func TestHostedAdmissionConfigProjectMerge(t *testing.T) {
	global := DefaultConfig()
	global.Orchestration.ProviderMaxActiveHostedRequests = map[string]int{"sample": 2, "other": 3}
	global.Orchestration.ProviderHostedRequestsPerMinute = map[string]int{"sample": 10, "other": 20}
	global.Orchestration.ProviderHostedRetriesPerMinute = map[string]int{"sample": 3, "other": 4}
	path := filepath.Join(t.TempDir(), "project.yaml")
	writeTestFile(t, path, `orchestration:
  provider_max_active_hosted_requests:
    sample: 0
  provider_hosted_requests_per_minute:
    sample: 5
  provider_hosted_retries_per_minute:
    sample: 1
`)
	_, merged, err := MergeProjectConfig(global, path)
	if err != nil {
		t.Fatal(err)
	}
	got := merged.Orchestration
	if got.ProviderMaxActiveHostedRequests["sample"] != 0 || got.ProviderMaxActiveHostedRequests["other"] != 3 || got.ProviderHostedRequestsPerMinute["sample"] != 5 || got.ProviderHostedRequestsPerMinute["other"] != 20 || got.ProviderHostedRetriesPerMinute["sample"] != 1 || got.ProviderHostedRetriesPerMinute["other"] != 4 {
		t.Fatalf("merged=%+v", got)
	}
	if global.Orchestration.ProviderMaxActiveHostedRequests["sample"] != 2 {
		t.Fatal("merge mutated global config")
	}
}
