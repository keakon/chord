package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
)

func usageSpeedStats() analytics.SessionStats {
	return analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
		"provider/model-1":      {ThroughputTokens: 200, ThroughputNs: int64(time.Second), ThroughputSamples: 2},
		"provider/model-2":      {ThroughputTokens: 100, ThroughputNs: int64(time.Second), ThroughputSamples: 1},
		"other/model-1":         {ThroughputTokens: 50, ThroughputNs: int64(time.Second), ThroughputSamples: 1},
		"provider/model-1@high": {ThroughputTokens: 25, ThroughputNs: int64(time.Second), ThroughputSamples: 1},
	}}
}

func TestModelSpeedLineSelectsExactModelRef(t *testing.T) {
	for _, tt := range []struct {
		ref  string
		want string
	}{
		{"provider/model-1", "200"},
		{"provider/model-2", "100"},
		{"other/model-1", "50"},
		{"provider/model-1@high", "25"},
	} {
		t.Run(tt.ref, func(t *testing.T) {
			text := strings.TrimSpace(ansi.Strip(renderModelSpeedLine(40, usageSpeedStats(), tt.ref)))
			if want := "tok/s: " + tt.want; text != want {
				t.Fatalf("speed line = %q, want %q", text, want)
			}
		})
	}
}

func TestModelSpeedLineHiddenWithoutSamples(t *testing.T) {
	if line := renderModelSpeedLine(40, analytics.SessionStats{}, "provider/model-1"); line != "" {
		t.Fatalf("empty stats rendered %q, want no line", line)
	}
	stats := analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
		"provider/model-1": {Calls: 3, OutputTokens: 500},
	}}
	if line := renderModelSpeedLine(40, stats, "provider/model-1"); line != "" {
		t.Fatalf("no throughput samples rendered %q, want no line", line)
	}
	if line := renderModelSpeedLine(40, usageSpeedStats(), "provider/model-3"); line != "" {
		t.Fatalf("unmeasured model rendered %q, want no line", line)
	}
	if line := renderModelSpeedLine(40, usageSpeedStats(), "provider/model-1@low"); line != "" {
		t.Fatalf("unmeasured variant rendered %q, want no line", line)
	}
}

func TestInfoPanelModelSpeedFollowsDisplayRef(t *testing.T) {
	backend := newAttemptRefAgent("provider/model-1", "provider/model-2")
	backend.state.PoolName = "default"
	backend.state.PoolNames = []string{"default"}
	backend.state.KeysConfirmed = 2
	backend.state.KeysTotal = 2
	backend.state.ServiceTier = config.ServiceTierFast
	backend.state.EffectiveTier = config.ServiceTierFast
	backend.usage = usageSpeedStats()
	backend.usage.OutputTokens = 375
	backend.usage.LLMCalls = 5
	m := NewModelWithSize(backend, 100, 24)

	text := strings.TrimSpace(ansi.Strip(m.buildInfoPanelModelBlock(40, backend.usage)))
	if !strings.HasSuffix(text, "tok/s: 100") {
		t.Fatalf("model block = %q, want displayed model speed after pool, keys and tier", text)
	}
	for _, label := range []string{"Pool: default", "Keys: 2/2", "tier: fast"} {
		if !strings.Contains(text, label) {
			t.Fatalf("model block = %q, want %q before speed", text, label)
		}
	}
	if usage := ansi.Strip(m.buildInfoPanelUsageBlock(40, 34, backend.usage)); strings.Contains(usage, "tok/s") || !strings.Contains(usage, "TOKENS") {
		t.Fatalf("usage block = %q, want tokens without model speed", usage)
	}

	backend.state.DisplayRef = "other/model-1"
	text = strings.TrimSpace(ansi.Strip(m.buildInfoPanelModelBlock(40, backend.usage)))
	if !strings.HasSuffix(text, "tok/s: 50") {
		t.Fatalf("model block after provider switch = %q, want other provider speed", text)
	}
	backend.state.DisplayRef = "provider/model-3"
	if text := ansi.Strip(m.buildInfoPanelModelBlock(40, backend.usage)); strings.Contains(text, "tok/s") {
		t.Fatalf("model block without samples = %q, want hidden speed", text)
	}
}

func TestInfoPanelModelSpeedInvalidatesCachedPanel(t *testing.T) {
	backend := newAttemptRefAgent("provider/model-1", "provider/model-1")
	backend.usage = usageSpeedStats()
	m := NewModelWithSize(backend, 100, 40)
	first := m.renderInfoPanel(40, 40)
	backend.usage.ByModel["provider/model-1"].ThroughputNs = int64(2 * time.Second)
	second := m.renderInfoPanel(40, 40)
	if first == second || !strings.Contains(ansi.Strip(second), "tok/s: 100") {
		t.Fatalf("updated panel = %q, want speed refreshed to 100", ansi.Strip(second))
	}
	backend.usage.ByModel["provider/model-1"].ThroughputSamples = 0
	if text := ansi.Strip(m.renderInfoPanel(40, 40)); strings.Contains(text, "tok/s") {
		t.Fatalf("panel without samples = %q, want hidden speed", text)
	}
}
