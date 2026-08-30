package tui

import (
	"strings"
	"testing"
	"time"
)

// Regression: when a tool header was too wide for the elapsed/progress suffix
// it fell back to stripANSI + runewidth.Truncate, which silently dropped the
// tool-name colour. Only the widest headers reach that branch, and mcp_* names
// are both the longest and carry the longest inline parameter summary, so they
// rendered in the terminal default colour while short built-in names kept the
// theme colour — a per-tool colour split with no styling code behind it.
func TestToolHeaderKeepsToolNameColourWhenTruncated(t *testing.T) {
	ApplyTheme(DefaultTheme())

	build := func(name, args string) *Block {
		return &Block{
			ID:         1,
			Type:       BlockToolCall,
			ToolName:   name,
			Content:    args,
			ResultDone: true,
			StartedAt:  time.Now(),
			SettledAt:  time.Now().Add(3 * time.Second),
		}
	}

	blocks := []*Block{
		build("mcp_sample__search_tool", `{"query":"sample memory research query","numResults":10,"model":"auto","text":true}`),
		build("web_fetch", `{"url":"https://example.invalid/papers/2310.06201"}`),
	}

	// 120 is the clamped card width used on wide terminals; it is what forces
	// the mcp_* header (long name + long inline param summary) to shrink while
	// the short built-in header still fits.
	const width = 120
	for _, b := range blocks {
		styled := ToolCallStyle.Render(b.ToolName)
		lines := b.Render(width, "")
		found := false
		for _, line := range lines {
			if strings.Contains(line, styled) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s lost its tool-name colour at width %d\nheader: %s",
				b.ToolName, width, strings.ReplaceAll(lines[0], "\x1b", "ESC"))
		}
	}
}

// Both MCP and built-in tool names must render with the same styled sequence at
// every width, which is the invariant the truncation fallback used to break.
func TestToolNameColourMatchesAcrossToolKinds(t *testing.T) {
	ApplyTheme(DefaultTheme())

	newBlock := func(name, args string) *Block {
		return &Block{
			ID:         1,
			Type:       BlockToolCall,
			ToolName:   name,
			Content:    args,
			ResultDone: true,
			StartedAt:  time.Now(),
			SettledAt:  time.Now().Add(3 * time.Second),
		}
	}

	for _, width := range []int{80, 100, 120, 160, 200} {
		mcp := newBlock("mcp_sample__search_tool", `{"query":"sample memory research query","numResults":10,"model":"auto","text":true}`)
		fetch := newBlock("web_fetch", `{"url":"https://example.invalid/papers/2310.06201"}`)

		var mcpLine, fetchLine string
		for _, line := range mcp.Render(width, "") {
			if strings.Contains(stripANSI(line), mcp.ToolName) {
				mcpLine = line
				break
			}
		}
		for _, line := range fetch.Render(width, "") {
			if strings.Contains(stripANSI(line), fetch.ToolName) {
				fetchLine = line
				break
			}
		}
		if mcpLine == "" || fetchLine == "" {
			t.Fatalf("width %d: header line not found (mcp=%q fetch=%q)", width, mcpLine, fetchLine)
		}
		mcpStyled := ToolCallStyle.Render(mcp.ToolName)
		fetchStyled := ToolCallStyle.Render(fetch.ToolName)
		if !strings.Contains(mcpLine, mcpStyled) {
			t.Fatalf("width %d: mcp header missing tool-name colour\n%s", width, strings.ReplaceAll(mcpLine, "\x1b", "ESC"))
		}
		if !strings.Contains(fetchLine, fetchStyled) {
			t.Fatalf("width %d: built-in header missing tool-name colour\n%s", width, strings.ReplaceAll(fetchLine, "\x1b", "ESC"))
		}
	}
}
