package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/analytics"
)

func usageSpeedStats() analytics.SessionStats {
	return analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
		"provider/model-1": {ThroughputTokens: 200, ThroughputNs: int64(time.Second), ThroughputSamples: 2},
		"provider/model-2": {ThroughputTokens: 100, ThroughputNs: int64(time.Second), ThroughputSamples: 1},
	}}
}

func TestUsageSpeedLineSumsAcrossModels(t *testing.T) {
	line := renderUsageSpeedLine(40, usageSpeedStats())
	text := ansi.Strip(line)
	if !strings.Contains(text, "tok/s") || !strings.Contains(text, "150") {
		t.Fatalf("speed line = %q, want tok/s 150", text)
	}
}

func TestUsageSpeedLineHiddenWithoutSamples(t *testing.T) {
	if line := renderUsageSpeedLine(40, analytics.SessionStats{}); line != "" {
		t.Fatalf("empty stats rendered %q, want no line", line)
	}
	stats := analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
		"provider/model-1": {Calls: 3, OutputTokens: 500},
	}}
	if line := renderUsageSpeedLine(40, stats); line != "" {
		t.Fatalf("no throughput samples rendered %q, want no line", line)
	}
}
