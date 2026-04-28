package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

type compactionBackend interface {
	Name() string
	ProduceSummary(client *llm.Client, fallbackModelRef, prompt string) (string, string, error)
}

type genericCompactionBackend struct {
	agent *MainAgent
}

func (b genericCompactionBackend) Name() string { return config.CompactionPresetGeneric }

func (b genericCompactionBackend) ProduceSummary(client *llm.Client, fallbackModelRef, prompt string) (string, string, error) {
	return b.agent.callCompactionSummary(client, fallbackModelRef, prompt)
}

type codexCompactionBackend struct {
	agent *MainAgent
}

func (b codexCompactionBackend) Name() string { return config.CompactionPresetCodex }

func (b codexCompactionBackend) ProduceSummary(client *llm.Client, fallbackModelRef, prompt string) (string, string, error) {
	return b.agent.callCompactionEndpoint(client, fallbackModelRef, prompt)
}

func (a *MainAgent) configuredCompactionPreset() string {
	for _, cfg := range []*config.Config{a.projectConfig, a.globalConfig} {
		if cfg == nil {
			continue
		}
		preset := strings.ToLower(strings.TrimSpace(cfg.Context.Compaction.Preset))
		switch preset {
		case config.CompactionPresetGeneric, config.CompactionPresetCodex:
			return preset
		}
	}
	return ""
}

func (a *MainAgent) selectCompactionBackend(client *llm.Client) compactionBackend {
	preset := a.configuredCompactionPreset()
	if preset == config.CompactionPresetGeneric {
		return genericCompactionBackend{agent: a}
	}
	if client != nil && client.SupportsCompactEndpoint() {
		return codexCompactionBackend{agent: a}
	}
	return genericCompactionBackend{agent: a}
}
