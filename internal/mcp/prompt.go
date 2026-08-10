package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ServerTools is one server row of the MCP discoverability prompt block:
// the server name plus its registered tool ids, or a note (error / no tools)
// when the tool list is unavailable.
type ServerTools struct {
	Name  string
	Tools []string
	Note  string
}

// serversPromptHeader is the fixed preamble of the MCP prompt block. The
// parser keys on its first line, so renderer and parser must only be changed
// together (guarded by the round-trip test).
const serversPromptHeader = "## MCP (Model Context Protocol) integrations\n" +
	"The following external servers are connected. Each MCP tool is named **`mcp_<server>_<tool>`** (use that exact id when calling). " +
	"When the user asks which MCPs you have, list **server names** and the **registered tool ids** under each.\n"

const serverRowPrefix = "- **"
const serverToolsSeparator = "** — tools: "
const serverNoteSeparator = "** — "

// ConnectedServersPromptBlock returns a system-prompt section listing each
// connected MCP server and its tool names so the model can answer questions
// like "which MCPs are available" without renaming tools.
func ConnectedServersPromptBlock(ctx context.Context, mgr *Manager) string {
	return RenderServersPromptBlock(connectedServersListing(ctx, mgr))
}

// connectedServersListing collects the per-server tool ids for every connected
// server, using cached tool definitions when available.
func connectedServersListing(ctx context.Context, mgr *Manager) []ServerTools {
	if mgr == nil {
		return nil
	}
	names := mgr.ServerNames()
	if len(names) == 0 {
		return nil
	}
	clients := mgr.Clients()
	if ctx == nil {
		ctx = context.Background()
	}
	servers := make([]ServerTools, 0, len(names))
	for _, srv := range names {
		c, ok := clients[srv]
		if !ok {
			continue
		}
		toolDefs := mgr.CachedToolDefs(srv)
		if len(toolDefs) == 0 {
			var err error
			toolDefs, err = c.ListTools(ctx)
			if err != nil {
				servers = append(servers, ServerTools{Name: srv, Note: fmt.Sprintf("(could not list tools: %v)", err)})
				continue
			}
			toolDefs = mgr.filterToolDefs(srv, toolDefs)
			mgr.setCachedToolDefs(srv, toolDefs)
		}
		tn := make([]string, 0, len(toolDefs))
		for _, t := range toolDefs {
			if t.Name != "" {
				tn = append(tn, RegisteredMCPToolName(srv, t.Name))
			}
		}
		sort.Strings(tn)
		servers = append(servers, ServerTools{Name: srv, Tools: tn})
	}
	return servers
}

// RenderServersPromptBlock renders the prompt block for the given server rows.
// Returns "" when there are no rows.
func RenderServersPromptBlock(servers []ServerTools) string {
	if len(servers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(serversPromptHeader)
	b.WriteString("\n")
	for _, srv := range servers {
		b.WriteString(serverRowPrefix)
		b.WriteString(srv.Name)
		switch {
		case len(srv.Tools) > 0:
			b.WriteString(serverToolsSeparator)
			b.WriteString(strings.Join(srv.Tools, ", "))
		case srv.Note != "":
			b.WriteString(serverNoteSeparator)
			b.WriteString(srv.Note)
		default:
			b.WriteString(serverNoteSeparator)
			b.WriteString("(no tools)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ParseServersPromptBlock parses a block produced by RenderServersPromptBlock
// back into server rows. Returns nil when the block does not carry this
// package's header (e.g. a custom or empty block), so callers can pass such
// blocks through untouched instead of guessing at their structure.
func ParseServersPromptBlock(block string) []ServerTools {
	headerFirstLine, _, _ := strings.Cut(serversPromptHeader, "\n")
	firstLine, _, _ := strings.Cut(strings.TrimSpace(block), "\n")
	if strings.TrimSpace(firstLine) != headerFirstLine {
		return nil
	}
	var servers []ServerTools
	for line := range strings.SplitSeq(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, serverRowPrefix) {
			continue
		}
		rest := trimmed[len(serverRowPrefix):]
		if name, toolList, ok := strings.Cut(rest, serverToolsSeparator); ok {
			row := ServerTools{Name: name}
			for tool := range strings.SplitSeq(toolList, ",") {
				if tool = strings.TrimSpace(tool); tool != "" {
					row.Tools = append(row.Tools, tool)
				}
			}
			servers = append(servers, row)
			continue
		}
		if name, note, ok := strings.Cut(rest, serverNoteSeparator); ok {
			servers = append(servers, ServerTools{Name: name, Note: note})
		}
	}
	return servers
}
