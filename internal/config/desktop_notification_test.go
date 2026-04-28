package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigYAML_DesktopNotificationScalar(t *testing.T) {
	const raw = `
providers:
  x:
    type: "chat-completions"
    models:
      m:
        limit: { context: 1, output: 1 }
desktop_notification: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.DesktopNotification == nil || !*cfg.DesktopNotification {
		t.Fatal("expected desktop_notification: true")
	}
}

func TestConfigYAML_DesktopNotificationFalse(t *testing.T) {
	const raw = `
providers:
  x:
    type: "chat-completions"
    models:
      m:
        limit: { context: 1, output: 1 }
desktop_notification: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.DesktopNotification == nil || *cfg.DesktopNotification {
		t.Fatal("expected desktop_notification: false")
	}
}

func TestConfigYAML_DesktopNotificationOmitted(t *testing.T) {
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
	if cfg.DesktopNotification != nil {
		t.Fatal("expected desktop_notification omitted (nil)")
	}
}
