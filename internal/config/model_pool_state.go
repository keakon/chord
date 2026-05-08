package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ModelPoolState struct {
	Version           int               `yaml:"version"`
	CurrentModelPool  string            `yaml:"current_model_pool,omitempty"`
	LegacyCurrentRole string            `yaml:"current_role,omitempty"`
	AgentOverrides    map[string]string `yaml:"agent_overrides,omitempty"`
}

const modelPoolStateVersion = 1

func ModelPoolStatePath(projectKey, stateDir string) string {
	return filepath.Join(stateDir, "projects", projectKey, "model_pool_state.yaml")
}

func LoadModelPoolState(path string) (*ModelPoolState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModelPoolState{Version: modelPoolStateVersion}, nil
		}
		return nil, fmt.Errorf("read model pool state: %w", err)
	}
	var state ModelPoolState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return &ModelPoolState{Version: modelPoolStateVersion}, nil
	}
	if state.Version == 0 {
		state.Version = modelPoolStateVersion
	}
	if state.CurrentModelPool == "" {
		state.CurrentModelPool = state.LegacyCurrentRole
	}
	state.LegacyCurrentRole = ""
	return &state, nil
}

func SaveModelPoolState(path string, state *ModelPoolState) error {
	if state == nil {
		return nil
	}
	state.Version = modelPoolStateVersion
	state.LegacyCurrentRole = ""

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create model pool state dir: %w", err)
	}

	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal model pool state: %w", err)
	}

	tmp := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write model pool state temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename model pool state: %w", err)
	}
	return nil
}
