package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAgentConfigIsSubAgentAndModelRefHelpers(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{name: "empty defaults subagent", mode: "", want: true},
		{name: "explicit subagent", mode: "subagent", want: true},
		{name: "primary", mode: "primary", want: false},
		{name: "case sensitive", mode: "SubAgent", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (&AgentConfig{Mode: tt.mode}).IsSubAgent(); got != tt.want {
				t.Fatalf("IsSubAgent() = %v, want %v", got, tt.want)
			}
		})
	}

	ref, variant := ParseModelRef("provider/model@fast")
	if ref != "provider/model" || variant != "fast" {
		t.Fatalf("ParseModelRef variant = (%q, %q)", ref, variant)
	}
	ref, variant = ParseModelRef("provider/model")
	if ref != "provider/model" || variant != "" {
		t.Fatalf("ParseModelRef no variant = (%q, %q)", ref, variant)
	}
	ref, variant = ParseModelRef("provider/model@variant@tail")
	if ref != "provider/model@variant" || variant != "tail" {
		t.Fatalf("ParseModelRef last @ = (%q, %q)", ref, variant)
	}
	if got := NormalizeModelRef("provider/model@fast"); got != "provider/model" {
		t.Fatalf("NormalizeModelRef() = %q, want provider/model", got)
	}
}

func TestLoadAgentConfigsSkipsUnsupportedFilesAndMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	configs, err := LoadAgentConfigs(missing)
	if err != nil {
		t.Fatalf("LoadAgentConfigs missing dir: %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("missing dir configs = %#v, want empty", configs)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.yaml"), []byte(`mode: subagent
model_pools: [default]
prompt: hello
`), 0o644); err != nil {
		t.Fatalf("WriteFile worker.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile notes.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("Mkdir nested: %v", err)
	}

	configs, err = LoadAgentConfigs(dir)
	if err != nil {
		t.Fatalf("LoadAgentConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("configs len = %d, want 1: %#v", len(configs), configs)
	}
	cfg := configs["worker"]
	if cfg == nil || cfg.Name != "worker" || cfg.SystemPrompt != "hello" {
		t.Fatalf("worker config = %#v", cfg)
	}
}

func TestLoadAgentConfigRejectsUnsupportedExtensionAndMissingModels(t *testing.T) {
	dir := t.TempDir()
	unsupported := filepath.Join(dir, "worker.txt")
	if err := os.WriteFile(unsupported, []byte("name: worker"), 0o644); err != nil {
		t.Fatalf("WriteFile unsupported: %v", err)
	}
	if _, err := LoadAgentConfig(unsupported); err == nil || !strings.Contains(err.Error(), "unsupported file extension") {
		t.Fatalf("LoadAgentConfig unsupported err = %v, want unsupported extension", err)
	}

	missingModels := filepath.Join(dir, "worker.yaml")
	if err := os.WriteFile(missingModels, []byte("name: worker\nmode: subagent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile missingModels: %v", err)
	}
	if _, err := LoadAgentConfig(missingModels); err == nil || !strings.Contains(err.Error(), "must define at least one model pool via model_pools") {
		t.Fatalf("LoadAgentConfig missing models err = %v, want missing model pool", err)
	}

	bothFields := filepath.Join(dir, "both.yaml")
	if err := os.WriteFile(bothFields, []byte("name: both\nmodels:\n  default:\n    - sample/model\nmodel_pools: [default]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile bothFields: %v", err)
	}
	if _, err := LoadAgentConfig(bothFields); err == nil || !strings.Contains(err.Error(), "inline models are not supported") {
		t.Fatalf("LoadAgentConfig both fields err = %v, want mutually exclusive", err)
	}

	emptyPoolRef := filepath.Join(dir, "empty_pool.yaml")
	if err := os.WriteFile(emptyPoolRef, []byte("name: empty_pool\nmodel_pools: [\"\"]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile emptyPoolRef: %v", err)
	}
	if _, err := LoadAgentConfig(emptyPoolRef); err == nil || !strings.Contains(err.Error(), "model_pools entry must not be empty") {
		t.Fatalf("LoadAgentConfig empty pool ref err = %v, want empty pool ref error", err)
	}
}

func TestBuiltinAndResolvedAgentConfigs(t *testing.T) {
	builtins := BuiltinAgentConfigs()
	if builtins["planner"] == nil || builtins["builder"] == nil {
		t.Fatalf("BuiltinAgentConfigs() = %#v, want planner and builder", builtins)
	}
	if builtins["planner"].Mode != "primary" || builtins["builder"].Mode != "primary" {
		t.Fatalf("builtin modes = planner:%q builder:%q", builtins["planner"].Mode, builtins["builder"].Mode)
	}
	if builtins["planner"].Permission.Kind != yaml.MappingNode || builtins["builder"].Permission.Kind != yaml.MappingNode {
		t.Fatalf("builtin permissions should be mapping nodes")
	}

	globalDir := t.TempDir()
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalDir, "reviewer.yaml"), []byte(`name: reviewer
mode: subagent
model_pools: [base]
prompt: global prompt
`), 0o644); err != nil {
		t.Fatalf("WriteFile global reviewer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "reviewer.yaml"), []byte(`name: reviewer
mode: subagent
model_pools: [fast, base]
prompt: project prompt
`), 0o644); err != nil {
		t.Fatalf("WriteFile project reviewer: %v", err)
	}

	resolved, err := ResolveAgentConfigs(projectDir, globalDir)
	if err != nil {
		t.Fatalf("ResolveAgentConfigs: %v", err)
	}
	if resolved["planner"] == nil || resolved["builder"] == nil {
		t.Fatalf("resolved missing builtins: %#v", resolved)
	}
	reviewer := resolved["reviewer"]
	if reviewer == nil || len(reviewer.ModelPools) != 2 || reviewer.ModelPools[0] != "fast" || reviewer.ModelPools[1] != "base" || reviewer.SystemPrompt != "project prompt" {
		t.Fatalf("reviewer config = %#v, want project override", reviewer)
	}
	if len(reviewer.poolOrder) != 2 || reviewer.poolOrder[0] != "fast" || reviewer.poolOrder[1] != "base" {
		t.Fatalf("reviewer poolOrder = %#v, want %v", reviewer.poolOrder, []string{"fast", "base"})
	}
}

func TestHookCommandYAMLBranches(t *testing.T) {
	var shell HookCommand
	if err := yaml.Unmarshal([]byte("echo hello"), &shell); err != nil {
		t.Fatalf("unmarshal scalar: %v", err)
	}
	if shell.Shell != "echo hello" || len(shell.Args) != 0 || shell.IsZero() {
		t.Fatalf("scalar HookCommand = %#v", shell)
	}
	marshaled, err := shell.MarshalYAML()
	if err != nil {
		t.Fatalf("MarshalYAML scalar: %v", err)
	}
	if marshaled != "echo hello" {
		t.Fatalf("MarshalYAML scalar = %#v", marshaled)
	}

	var args HookCommand
	if err := yaml.Unmarshal([]byte("[go, test, ./...]"), &args); err != nil {
		t.Fatalf("unmarshal sequence: %v", err)
	}
	if args.Shell != "" || len(args.Args) != 3 || args.Args[0] != "go" || args.IsZero() {
		t.Fatalf("sequence HookCommand = %#v", args)
	}
	marshaled, err = args.MarshalYAML()
	if err != nil {
		t.Fatalf("MarshalYAML sequence: %v", err)
	}
	gotArgs, ok := marshaled.([]string)
	if !ok || len(gotArgs) != 3 || gotArgs[2] != "./..." {
		t.Fatalf("MarshalYAML sequence = %#v", marshaled)
	}

	var invalid HookCommand
	if err := yaml.Unmarshal([]byte("{bad: value}"), &invalid); err == nil || !strings.Contains(err.Error(), "hook command must be") {
		t.Fatalf("unmarshal mapping err = %v, want hook command error", err)
	}
	if !(HookCommand{}).IsZero() {
		t.Fatal("zero HookCommand should be zero")
	}
}

func TestResolveAgentModelPools(t *testing.T) {
	globalPools := map[string][]string{
		"base":   {"provider/base-model"},
		"fast":   {"provider/fast-model"},
		"strong": {"provider/strong-model"},
	}

	t.Run("resolves references into Models", func(t *testing.T) {
		agents := map[string]*AgentConfig{
			"worker": {
				Name:       "worker",
				ModelPools: []string{"base", "fast"},
			},
		}
		if err := ResolveAgentModelPools(agents, globalPools); err != nil {
			t.Fatalf("ResolveAgentModelPools: %v", err)
		}
		cfg := agents["worker"]
		if len(cfg.ModelPools) != 0 {
			t.Fatalf("ModelPools should be cleared after resolution, got %v", cfg.ModelPools)
		}
		if len(cfg.Models) != 2 {
			t.Fatalf("Models len = %d, want 2", len(cfg.Models))
		}
		if len(cfg.Models["base"]) != 1 || cfg.Models["base"][0] != "provider/base-model" {
			t.Fatalf("base pool = %v", cfg.Models["base"])
		}
		if len(cfg.Models["fast"]) != 1 || cfg.Models["fast"][0] != "provider/fast-model" {
			t.Fatalf("fast pool = %v", cfg.Models["fast"])
		}
	})

	t.Run("skips agents with inline Models", func(t *testing.T) {
		agents := map[string]*AgentConfig{
			"worker": {
				Name: "worker",
				Models: map[string][]string{
					"default": {"provider/model"},
				},
			},
		}
		if err := ResolveAgentModelPools(agents, globalPools); err != nil {
			t.Fatalf("ResolveAgentModelPools: %v", err)
		}
		if len(agents["worker"].Models["default"]) != 1 || agents["worker"].Models["default"][0] != "provider/model" {
			t.Fatalf("inline Models should be preserved, got %v", agents["worker"].Models)
		}
	})

	t.Run("error on undefined pool reference", func(t *testing.T) {
		agents := map[string]*AgentConfig{
			"worker": {
				Name:       "worker",
				ModelPools: []string{"nonexistent"},
			},
		}
		if err := ResolveAgentModelPools(agents, globalPools); err == nil || !strings.Contains(err.Error(), "not defined in config model_pools") {
			t.Fatalf("undefined pool err = %v, want not defined error", err)
		}
	})

	t.Run("error on empty global pool", func(t *testing.T) {
		poolsWithEmpty := map[string][]string{
			"empty": {},
		}
		agents := map[string]*AgentConfig{
			"worker": {
				Name:       "worker",
				ModelPools: []string{"empty"},
			},
		}
		if err := ResolveAgentModelPools(agents, poolsWithEmpty); err == nil || !strings.Contains(err.Error(), "which is empty") {
			t.Fatalf("empty pool err = %v, want empty error", err)
		}
	})

	t.Run("error on invalid model ref in global pool", func(t *testing.T) {
		poolsWithBadRef := map[string][]string{
			"bad": {"no-slash"},
		}
		agents := map[string]*AgentConfig{
			"worker": {
				Name:       "worker",
				ModelPools: []string{"bad"},
			},
		}
		if err := ResolveAgentModelPools(agents, poolsWithBadRef); err == nil || !strings.Contains(err.Error(), "must contain '/'") {
			t.Fatalf("bad ref err = %v, want must contain / error", err)
		}
	})

	t.Run("error with empty global pools", func(t *testing.T) {
		agents := map[string]*AgentConfig{
			"worker": {
				Name:       "worker",
				ModelPools: []string{"base"},
			},
		}
		if err := ResolveAgentModelPools(agents, nil); err == nil || !strings.Contains(err.Error(), "not defined in config model_pools") {
			t.Fatalf("ResolveAgentModelPools with nil global err = %v, want undefined pool error", err)
		}
	})

	t.Run("error on empty top-level pool name", func(t *testing.T) {
		poolsWithEmptyName := map[string][]string{
			"":     {"provider/model"},
			"base": {"provider/base-model"},
		}
		agents := map[string]*AgentConfig{
			"worker": {
				Name:       "worker",
				ModelPools: []string{"base"},
			},
		}
		if err := ResolveAgentModelPools(agents, poolsWithEmptyName); err == nil || !strings.Contains(err.Error(), "pool name must not be empty") {
			t.Fatalf("ResolveAgentModelPools with empty top-level pool name err = %v, want empty pool name error", err)
		}
	})
}

func TestValidateResolvedAgentModelPoolsRequiresNonEmptyPool(t *testing.T) {
	agents := map[string]*AgentConfig{
		"worker": {
			Name:   "worker",
			Models: map[string][]string{"empty": {}},
		},
	}
	if err := ValidateResolvedAgentModelPools(agents); err == nil || !strings.Contains(err.Error(), "must define at least one non-empty model pool") {
		t.Fatalf("ValidateResolvedAgentModelPools err = %v, want non-empty pool error", err)
	}
}
