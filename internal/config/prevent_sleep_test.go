package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigYAML_PreventSleepTrue(t *testing.T) {
	const raw = `
providers:
  x:
    type: "chat-completions"
    models:
      m:
        limit: { context: 1, output: 1 }
prevent_sleep: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PreventSleep == nil || !*cfg.PreventSleep {
		t.Fatal("expected prevent_sleep: true")
	}
}

func TestConfigYAML_PreventSleepFalse(t *testing.T) {
	const raw = `
providers:
  x:
    type: "chat-completions"
    models:
      m:
        limit: { context: 1, output: 1 }
prevent_sleep: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PreventSleep == nil || *cfg.PreventSleep {
		t.Fatal("expected prevent_sleep: false")
	}
}

func TestConfigYAML_PreventSleepOmitted(t *testing.T) {
	const raw = `
providers:
  x:
    type: "chat-completions"
    models:
      m:
        limit: { context: 1, output: 1 }
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PreventSleep != nil {
		t.Fatal("expected prevent_sleep omitted (nil)")
	}
}
