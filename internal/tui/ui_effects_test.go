package tui

import "testing"

func TestUIEffectsMergeAndApplyInvalidatesUsageWithSidebarRefresh(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.cachedInfoPanelFP = "fp"
	m.cachedInfoPanelOut = "out"
	m.renderCacheState.statusBarAgentSnapshotDirty = false
	m.usageStats.linesCacheWidth = 120
	m.usageStats.linesCacheVer = 7
	m.usageStats.linesCacheLines = []string{"cached"}

	effects := uiEffects{refreshSidebar: true}
	m.applyUIEffects(effects)

	if !m.renderCacheState.statusBarAgentSnapshotDirty {
		t.Fatal("status bar agent snapshot was not invalidated")
	}
	if m.usageStats.linesCacheWidth != 0 || m.usageStats.linesCacheVer != 0 || m.usageStats.linesCacheLines != nil {
		t.Fatal("usage stats cache was not invalidated")
	}
	if m.cachedInfoPanelFP != "" || m.cachedInfoPanelOut != "" {
		t.Fatalf("info panel cache = (%q, %q), want cleared", m.cachedInfoPanelFP, m.cachedInfoPanelOut)
	}
}

func TestUIEffectsMergeCombinesFlags(t *testing.T) {
	var effects uiEffects
	effects.merge(uiEffects{refreshSidebar: true})
	effects.merge(uiEffects{invalidateUsage: true})
	if !effects.refreshSidebar || !effects.invalidateUsage {
		t.Fatalf("merged effects = %+v, want both flags", effects)
	}
}
