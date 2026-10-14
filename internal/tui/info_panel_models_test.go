package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestKeyPoolTickWakesCodexRateLimitPollingWhileAgentActive(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{CapturedAt: time.Now().Add(-codexActiveRateLimitPollInterval)}
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	updated, _ := m.Update(keyPoolTickMsg{gen: m.keyPoolTickGen})
	m = *(updated.(*Model))

	if backend.wakeRateLimitCalls != 1 {
		t.Fatalf("WakeCodexRateLimitPolling calls = %d, want 1", backend.wakeRateLimitCalls)
	}

	backend.rateLimitSnapshot.CapturedAt = time.Now()
	_, _ = m.Update(keyPoolTickMsg{gen: m.keyPoolTickGen})
	if backend.wakeRateLimitCalls != 1 {
		t.Fatalf("WakeCodexRateLimitPolling calls with fresh snapshot = %d, want 1", backend.wakeRateLimitCalls)
	}
}

func TestRenderInfoPanelShowsKeyPoolConfirmedCount(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "openai/test"
	backend.keysConfirmed = 3
	backend.keysTotal = 5
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "Keys: 3/5") {
		t.Fatalf("rendered info panel should show confirmed/total; got %q", plain)
	}
	if strings.Contains(plain, "Recovering") {
		t.Fatalf("rendered info panel should not show Recovering line; got %q", plain)
	}
}

// TestRenderInfoPanelPoolMatchesProviderLabelStyle verifies the Pool line uses
// the same key/value styling as the Provider line: a dim "Pool:" label with an
// currentStyles.InfoPanelValue pool name.
func TestRenderInfoPanelPoolMatchesProviderLabelStyle(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.poolNamesByFocus = map[string][]string{"": {"thinking", "non-thinking"}}
	backend.currentPoolByFocus = map[string]string{"": "thinking"}
	backend.keysConfirmed = 1
	backend.keysTotal = 3
	m := NewModel(backend)

	rendered := m.renderInfoPanel(40, 20)
	want := currentStyles.InfoPanelDim.Render("Pool: ") + currentStyles.InfoPanelValue.Render("thinking")
	if !strings.Contains(rendered, want) {
		t.Fatalf("Pool line should use dim label + currentStyles.InfoPanelValue name like Provider; rendered=%q want fragment=%q", rendered, want)
	}
	if strings.Contains(rendered, currentStyles.InfoPanelValue.Render("Pool: thinking")) {
		t.Fatalf("Pool label must not be bolded like currentStyles.InfoPanelValue; rendered=%q", rendered)
	}
	warn := currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelKeyWarnFg)).Render("Pool: thinking")
	if strings.Contains(rendered, warn) {
		t.Fatalf("Pool line should not use key warning color; rendered=%q warn fragment=%q", rendered, warn)
	}
}

func TestRenderInfoPanelShowsSingleModelPool(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "sample/gpt-5.6-sol@high"
	backend.poolNamesByFocus = map[string][]string{"": {"gpt-5.6-sol"}}
	backend.currentPoolByFocus = map[string]string{"": "gpt-5.6-sol"}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "Pool: gpt-5.6-sol") {
		t.Fatalf("single Pool should remain visible; got %q", plain)
	}
}

func TestKeyPoolHealthSeverityThresholds(t *testing.T) {
	tests := []struct {
		name    string
		healthy int
		total   int
		want    keyPoolSeverity
	}{
		{name: "zero total defaults normal", healthy: 0, total: 0, want: keyPoolSeverityNormal},
		{name: "two of two healthy is normal", healthy: 2, total: 2, want: keyPoolSeverityNormal},
		{name: "one of two healthy is warn", healthy: 1, total: 2, want: keyPoolSeverityWarn},
		{name: "zero of two healthy is critical", healthy: 0, total: 2, want: keyPoolSeverityCritical},
		{name: "two of three healthy is normal", healthy: 2, total: 3, want: keyPoolSeverityNormal},
		{name: "one of three healthy is warn", healthy: 1, total: 3, want: keyPoolSeverityWarn},
		{name: "zero of three healthy is critical", healthy: 0, total: 3, want: keyPoolSeverityCritical},
		{name: "three of four healthy is normal", healthy: 3, total: 4, want: keyPoolSeverityNormal},
		{name: "two of four healthy is warn", healthy: 2, total: 4, want: keyPoolSeverityWarn},
		{name: "one of four healthy is critical", healthy: 1, total: 4, want: keyPoolSeverityCritical},
		{name: "zero of four healthy is critical", healthy: 0, total: 4, want: keyPoolSeverityCritical},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyPoolHealthSeverity(tc.healthy, tc.total); got != tc.want {
				t.Fatalf("keyPoolHealthSeverity(%d, %d) = %v, want %v", tc.healthy, tc.total, got, tc.want)
			}
		})
	}
}

func TestRenderInfoPanelRateLimitHidesPrimaryWithNoMeaningfulData(t *testing.T) {
	cases := []struct {
		name   string
		window ratelimit.RateLimitWindow
	}{
		{
			name:   "zero without reset",
			window: ratelimit.RateLimitWindow{UsedPct: 0, WindowMinutes: 5 * 60},
		},
		{
			name:   "unknown with reset",
			window: ratelimit.RateLimitWindow{UsedPct: -1, WindowMinutes: 5 * 60, ResetsAt: time.Now().Add(time.Hour)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newInfoPanelAgent()
			window := tc.window
			backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{Primary: &window}

			m := NewModel(backend)
			section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "RATE LIMIT")
			if len(section) != 0 {
				t.Fatalf("rate limit section = %#v, want hidden", section)
			}
		})
	}
}

func TestRenderInfoPanelRateLimitHidesSecondaryWhenZeroAndDropsPrimaryLabel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{
			UsedPct:       42,
			WindowMinutes: 5 * 60,
			ResetsAt:      time.Now().Add(2*time.Hour + 30*time.Minute),
		},
		Secondary: &ratelimit.RateLimitWindow{
			UsedPct:       0,
			WindowMinutes: 7 * 24 * 60,
			ResetsAt:      time.Now().Add(3 * 24 * time.Hour),
		},
	}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "RATE LIMIT")
	if len(section) != 1 {
		t.Fatalf("rate limit lines = %#v, want one line", section)
	}
	if got := section[0]; got != "42% 2h30m" {
		t.Fatalf("rate limit line = %q, want %q", got, "42% 2h30m")
	}
}

func TestRenderInfoPanelRateLimitShowsWindowLabelsAndUnifiedResetFormat(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{
			UsedPct:       42,
			WindowMinutes: 5 * 60,
			ResetsAt:      time.Now().Add(2*time.Hour + 30*time.Minute),
		},
		Secondary: &ratelimit.RateLimitWindow{
			UsedPct:       75,
			WindowMinutes: 7 * 24 * 60,
			ResetsAt:      time.Now().Add(3*24*time.Hour + 4*time.Hour),
		},
	}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "RATE LIMIT")
	want := []string{"5h: 42% 2h30m", "1w: 75% 3d4h"}
	if len(section) != len(want) {
		t.Fatalf("rate limit lines = %#v, want %#v", section, want)
	}
	for i, line := range want {
		if section[i] != line {
			t.Fatalf("rate limit line %d = %q, want %q", i, section[i], line)
		}
	}
}

func TestFormatRateLimitResetTimeUsesDayHourAndHourMinute(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "expired", d: -1 * time.Second, want: ""},
		{name: "under1m", d: 45 * time.Second, want: "45s"},
		{name: "under24h", d: 2*time.Hour + 30*time.Minute, want: "2h30m"},
		{name: "over24h", d: 49*time.Hour + 15*time.Minute, want: "2d1h"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := ratelimit.RateLimitWindow{ResetsAt: time.Now().Add(tc.d)}
			if got := formatRateLimitResetTime(w); got != tc.want {
				t.Fatalf("formatRateLimitResetTime(%s) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestNextRateLimitSnapshotDisplayTransitionUsesSecondAndMinuteGranularity(t *testing.T) {
	now := time.Now()
	snap := &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{ResetsAt: now.Add(45*time.Second + 200*time.Millisecond)},
	}
	if got := nextRateLimitSnapshotDisplayTransition(snap, now); got <= 0 || got > time.Second {
		t.Fatalf("second-granularity transition = %v, want within 1s", got)
	}

	snap = &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{ResetsAt: now.Add(2*time.Hour + 30*time.Minute + 15*time.Second)},
	}
	if got := nextRateLimitSnapshotDisplayTransition(snap, now); got < 44*time.Second || got > 46*time.Second {
		t.Fatalf("minute-granularity transition = %v, want about 45s", got)
	}
}

func TestRenderInfoPanelModelLineShowsModelVariantAndProvider(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.runningModelRef = "sample/gpt-5.5@xhigh"
	m := NewModel(backend)
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 2 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "gpt-5.5@xhigh" {
		t.Fatalf("model line = %q, want gpt-5.5@xhigh", got)
	}
	if got := modelLines[1]; got != "Provider: sample" {
		t.Fatalf("provider line = %q, want Provider: sample", got)
	}
}

func TestRenderInfoPanelModelLineDoesNotLeakVariantToFallbackModel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "sample/gpt-5.5@xhigh"
	backend.runningModelRef = "sample/glm-5.1"
	backend.runningVariant = "xhigh"
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 2 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "glm-5.1" {
		t.Fatalf("model line = %q, want glm-5.1", got)
	}
	if got := modelLines[1]; got != "Provider: sample" {
		t.Fatalf("provider line = %q, want Provider: sample", got)
	}
}

func TestRenderInfoPanelModelLineShowsFallbackVariantWhenRunningRefIncludesVariant(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "sample/gpt-5.5@xhigh"
	backend.runningModelRef = "sample/glm-5.1@high"
	backend.runningVariant = "xhigh"
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 2 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "glm-5.1@high" {
		t.Fatalf("model line = %q, want glm-5.1@high", got)
	}
	if got := modelLines[1]; got != "Provider: sample" {
		t.Fatalf("provider line = %q, want Provider: sample", got)
	}
}

func TestRenderInfoPanelIdleModelLineUsesNextRequestModel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "provider-a/model-a"
	backend.runningModelRef = "provider-b/model-b"
	backend.nextRequestModelRef = "provider-b/model-b"
	backend.keysConfirmed = 5
	backend.keysTotal = 5
	m := NewModel(backend)

	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 3 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "model-b" {
		t.Fatalf("model line = %q, want model-b", got)
	}
	if got := modelLines[1]; got != "Provider: provider-b" {
		t.Fatalf("provider line = %q, want Provider: provider-b", got)
	}
	if got := modelLines[2]; got != "Keys: 5/5" {
		t.Fatalf("keys line = %q, want Keys: 5/5", got)
	}
}

func TestRenderInfoPanelBusyModelLineIgnoresNextRequestModel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "provider-a/model-a"
	backend.runningModelRef = "provider-a/model-a"
	backend.nextRequestModelRef = "provider-b/model-b"
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 2 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "model-a" {
		t.Fatalf("model line = %q, want model-a", got)
	}
	if got := modelLines[1]; got != "Provider: provider-a" {
		t.Fatalf("provider line = %q, want Provider: provider-a", got)
	}
}

func TestRenderInfoPanelModelLineHidesVariantWhenWidthIsShort(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.runningModelRef = "sample/supercalifragilistic-model@xhigh"
	m := NewModel(backend)
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(32, 20)), "MODEL")
	if len(modelLines) < 2 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "supercalifragilistic-model" {
		t.Fatalf("model line = %q, want variant hidden: supercalifragilistic-model", got)
	}
	if got := modelLines[1]; got != "Provider: sample" {
		t.Fatalf("provider line = %q, want Provider: sample", got)
	}
}
