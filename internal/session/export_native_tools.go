package session

import (
	"strings"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// The readable transcript is also the compaction input. Keep server execution
// and unknown outcomes visible without exposing opaque protocol replay blocks.
func writeNativeToolMarkdown(out *strings.Builder, native *message.NativeToolHistory) {
	for _, display := range tools.NativeToolDisplays(native) {
		out.WriteString(convformat.ToolCallMarkdown(display.Name, display.Args, nil, "", ""))
		out.WriteString("\n\n")
		out.WriteString(convformat.ToolResultMarkdown(display.Name, display.Result, ""))
		out.WriteString("\n\n")
	}
	if native != nil && !native.OutcomeUnknown {
		for _, call := range native.Calls {
			if len(call.Result) == 0 && call.Error == "" {
				out.WriteString(convformat.ToolCallMarkdown(tools.NameWebSearch, string(call.Input), nil, "", ""))
				out.WriteString("\n\nServer-side search is pending; no result has been reported.\n\n")
			}
		}
	}
}
