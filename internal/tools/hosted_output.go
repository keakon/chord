package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
)

const hostedOutputArtifactPrefix = "Full hosted output artifact (" + NameReadArtifact + " path): "

// renderResult sends configured image outputs to the existing attachment sink
// and preserves oversized native output as an immutable session artifact.
func (t HostedTool) renderResult(ctx context.Context, obs *message.HostedObservation) (string, error) {
	var output string
	if t.spec.Format != nil {
		output = t.spec.Format(obs)
	} else {
		output = formatHostedObservation(obs)
	}
	var notices []string
	for _, call := range obs.Calls {
		if len(call.Result) == 0 || len(t.spec.ImagePaths) == 0 {
			continue
		}
		var result any
		if err := json.Unmarshal(call.Result, &result); err != nil {
			return "", fmt.Errorf("decode hosted result: %w", err)
		}
		for _, path := range t.spec.ImagePaths {
			value, ok := hostedOutputPath(result, path)
			if !ok {
				continue
			}
			encoded, ok := value.(string)
			if !ok {
				notices = append(notices, "Image output at "+path+" is not a base64 string")
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				notices = append(notices, "Image output at "+path+" has invalid base64")
				continue
			}
			normalized, mime, err := imageutil.NormalizeImageBytes(ctx, raw, "")
			if err != nil {
				notices = append(notices, "Image output at "+path+" could not be decoded: "+err.Error())
				continue
			}
			if sink, ok := ImageSinkFromContext(ctx); ok {
				sink.AddImage(message.ContentPart{Type: message.ContentPartImage, MimeType: mime, Data: normalized})
			} else {
				notices = append(notices, "Image output at "+path+" has no attachment channel")
			}
		}
	}
	if hostedOutputNeedsArtifact(obs) {
		if dir := SessionDirFromContext(ctx); dir != "" {
			raw, err := json.Marshal(obs)
			if err != nil {
				return "", fmt.Errorf("marshal hosted output: %w", err)
			}
			ref, _, err := SaveImmutableResult(dir, "hosted_tool_output", raw)
			if err != nil {
				return "", fmt.Errorf("preserve hosted output: %w", err)
			}
			notices = append(notices, hostedOutputArtifactPrefix+ref.RelPath)
		} else {
			notices = append(notices, "Full native output is unavailable; no session artifact directory is available")
		}
	}
	if len(notices) > 0 {
		output += "\n" + strings.Join(notices, "\n")
	}
	return output, nil
}

// hostedOutputPath selects an object field using dot-separated keys. Array
// traversal is deliberately absent; the path applies to every completed call.
func hostedOutputPath(root any, path string) (any, bool) {
	node := root
	for key := range strings.SplitSeq(path, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		node, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return node, true
}

// Native messages include annotations and file references absent from the
// generic call preview. Preserve them as well as individually truncated calls.
func hostedOutputNeedsArtifact(obs *message.HostedObservation) bool {
	for _, c := range obs.Calls {
		if len(c.Input) > hostedPayloadSnippetBytes || len(c.Result) > hostedPayloadSnippetBytes {
			return true
		}
	}
	return len(obs.Items) > 0
}
