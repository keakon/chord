package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestMergeProjectConfigHostedModelPool(t *testing.T) {
	for _, tc := range []struct {
		name, override, want string
	}{
		{name: "inherit", override: "description: Project search", want: "global-tools"},
		{name: "replace", override: "model_pool: project-tools", want: "project-tools"},
		{name: "clear", override: "model_pool: ''"},
		{name: "whitespace", override: "model_pool: '   '", want: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := DefaultConfig()
			base.HostedTools = map[string]HostedToolConfig{
				"web_search": {ModelPool: "global-tools", TimeoutSeconds: 15},
			}
			base.ModelPools = map[string][]string{"global-tools": {"sample/model-1"}}
			path := filepath.Join(t.TempDir(), "project.yaml")
			writeTestFile(t, path, "hosted_tools:\n  web_search:\n    "+tc.override+"\nmodel_pools:\n  project-tools: [sample/model-2]\n")
			_, merged, err := MergeProjectConfig(base, path)
			if err != nil {
				t.Fatal(err)
			}
			got := merged.HostedTools["web_search"]
			if got.ModelPool != tc.want || got.TimeoutSeconds != 15 {
				t.Fatalf("merged hosted tool = %#v, want pool %q and inherited timeout", got, tc.want)
			}
			wantPools := map[string][]string{"global-tools": {"sample/model-1"}, "project-tools": {"sample/model-2"}}
			if !reflect.DeepEqual(merged.ModelPools, wantPools) {
				t.Fatalf("merged pools = %#v", merged.ModelPools)
			}
			if base.HostedTools["web_search"].ModelPool != "global-tools" {
				t.Fatal("merge changed global hosted routing")
			}
		})
	}
}
