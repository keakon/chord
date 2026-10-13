package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestInputHistoryConfig(t *testing.T) {
	cfg := InputHistoryConfig{}
	o, err := cfg.Effective()
	if err != nil || !o.Enabled || o.MaxEntries != 10000 || o.MaxBytes != 32<<20 {
		t.Fatal(o, err)
	}
	cfg.Enabled = new(false)
	cfg.MaxEntries = new(0)
	if _, err = cfg.Effective(); err == nil {
		t.Fatal("zero accepted")
	}
	cfg.MaxEntries = new(100001)
	if _, err = cfg.Effective(); err == nil {
		t.Fatal("overflow accepted")
	}
	cfg.MaxEntries = new(12)
	cfg.MaxBytes = new(int64(-1))
	if _, err = cfg.Effective(); err == nil {
		t.Fatal("negative accepted")
	}
	cfg.MaxBytes = new(int64(1024))
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded InputHistoryConfig
	if err = yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	o, err = decoded.Effective()
	if err != nil || o.Enabled || o.MaxEntries != 12 {
		t.Fatal(o, err)
	}
	if !projectIgnoredTopLevelKeys["input_history"] {
		t.Fatal("not user scoped")
	}
}

func TestInputHistoryProjectConfigIgnored(t *testing.T) {
	base := DefaultConfig()
	base.InputHistory.Enabled = new(false)
	base.InputHistory.MaxEntries = new(42)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("input_history:\n  enabled: true\n  max_entries: 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, merged, err := MergeProjectConfig(base, path)
	if err != nil {
		t.Fatal(err)
	}
	o, err := merged.InputHistory.Effective()
	if err != nil || o.Enabled || o.MaxEntries != 42 {
		t.Fatal(o, err)
	}
}
