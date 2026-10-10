package tools

import (
	"fmt"

	"github.com/keakon/chord/internal/message"
)

const unnamedNativeTool = "server_tool"

type NativeToolDisplay struct {
	ID, Name, Args, Result, Status string
	Parts                          []message.ContentPart
}

// NativeToolDisplays renders terminal server receipts without inventing local
// tool calls. Both live UI events and transcript restoration use this view.
func NativeToolDisplays(native *message.NativeToolHistory) []NativeToolDisplay {
	if native == nil {
		return nil
	}
	var out []NativeToolDisplay
	unknownShown := false
	seen := make(map[string]bool)
	for i, call := range native.Calls {
		unknownCall := len(call.Result) == 0 && call.Error == ""
		if unknownCall {
			if !native.OutcomeUnknown {
				continue
			}
			call.Error = "Outcome unknown; execution and charges may have occurred. Automatic replay is disabled."
		}
		id := call.ID
		if id == "" {
			if len(native.RequestIDs) == 0 {
				continue
			}
			id = fmt.Sprintf("%s:%d", native.RequestIDs[0], i)
		}
		id = "native:" + native.Target + ":" + id
		if seen[id] {
			continue
		}
		seen[id] = true
		status := string(message.ToolResultClassSuccess)
		if call.Error != "" {
			status = string(message.ToolResultClassError)
		}
		if call.Status == "cancelled" {
			status = string(message.ToolResultClassCancelled)
		}
		args := string(call.Input)
		if args == "" {
			args = "{}"
		}
		name := call.Name
		if call.Kind == "image_generation_call" {
			name = NameGenerateImage
		}
		if name == "" {
			name = nativeDisplayName(native)
		}
		observation := &message.HostedObservation{Calls: []message.HostedCall{call}}
		var result string
		if name == NameWebSearch {
			observation.Items = native.Items
			result = formatWebSearchObservation(observation)
		} else {
			result = formatHostedObservation(observation)
		}
		if name == NameGenerateImage && call.Error == "" {
			result = string(call.Result)
		} else {
			result = "Server-side tool\n" + result
		}
		out = append(out, NativeToolDisplay{ID: id, Name: name, Args: args, Result: result, Status: status, Parts: call.Parts})
		unknownShown = unknownShown || (native.OutcomeUnknown && unknownCall)
	}
	if native.OutcomeUnknown && !unknownShown && len(native.RequestIDs) > 0 {
		out = append(out, NativeToolDisplay{ID: "native:" + native.Target + ":request:" + native.RequestIDs[0], Name: nativeDisplayName(native), Args: "{}", Status: message.ToolStatusError, Result: "Server-side tool request: outcome unknown; execution and charges may have occurred. Automatic replay is disabled."})
	}
	return out
}

func nativeDisplayName(native *message.NativeToolHistory) string {
	if native.Authorization.Tool != "" {
		return native.Authorization.Tool
	}
	return unnamedNativeTool
}
