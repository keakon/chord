package acpagent

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
)

// messageParts converts ACP prompt content blocks into Chord content parts.
//
// Text, resource links and images are carried; a resource link that is not a
// readable local file degrades to a text reference the model can still see.
// Blocks Chord cannot carry at all — audio, blob resources, anything unknown —
// are rejected rather than silently dropped, so the client learns the agent
// cannot use them.
func messageParts(blocks []acp.ContentBlock) ([]message.ContentPart, error) {
	parts := make([]message.ContentPart, 0, len(blocks))
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			text := block.Text.Text
			if strings.TrimSpace(text) == "" {
				continue
			}
			parts = append(parts, message.ContentPart{Type: message.ContentPartText, Text: text})

		case block.Image != nil:
			if strings.TrimSpace(block.Image.Data) == "" {
				return nil, acp.NewInvalidParams(map[string]any{"error": "image block carries no data"})
			}
			data, err := base64.StdEncoding.DecodeString(block.Image.Data)
			if err != nil {
				return nil, acp.NewInvalidParams(map[string]any{"error": "image block is not valid base64"})
			}
			mimeType := strings.TrimSpace(block.Image.MimeType)
			if mimeType == "" {
				mimeType = http.DetectContentType(data)
			}
			parts = append(parts, message.ContentPart{
				Type:     message.ContentPartImage,
				MimeType: mimeType,
				Data:     data,
			})

		case block.ResourceLink != nil:
			parts = append(parts, resourceLinkParts(*block.ResourceLink)...)

		case block.Audio != nil:
			return nil, acp.NewInvalidParams(map[string]any{"error": "audio prompt blocks are not supported"})

		case block.Resource != nil:
			text, ok := embeddedResourceText(*block.Resource)
			if !ok {
				return nil, acp.NewInvalidParams(map[string]any{"error": "embedded resource prompt blocks must carry text"})
			}
			parts = append(parts, message.ContentPart{Type: message.ContentPartText, Text: text})

		default:
			return nil, acp.NewInvalidParams(map[string]any{"error": "unsupported prompt content block"})
		}
	}
	if len(parts) == 0 {
		return nil, acp.NewInvalidParams(map[string]any{"error": "prompt carries no supported content"})
	}
	return parts, nil
}

// resourceLinkParts turns a resource link into prompt context. A readable local
// file becomes the same <file path="...">...</file> part the TUI's @-references
// produce, so the model can act on the file; anything else (remote URIs,
// unreadable or oversized files) degrades to a plain text reference instead of
// pretending its contents are available. The degradation is logged, so a file
// the user meant to attach does not just lose its contents silently.
func resourceLinkParts(link acp.ContentBlockResourceLink) []message.ContentPart {
	uri := strings.TrimSpace(link.Uri)
	if path, ok := filePathFromURI(uri); ok {
		parts := filectx.BuildFileParts([]string{path}, func(p string) string { return p })
		if len(parts) > 0 {
			return parts
		}
		log.Debugf("acp resource link sent as a text reference reason=file-not-loaded uri=%v", uri)
	} else {
		log.Debugf("acp resource link sent as a text reference reason=not-a-local-file uri=%v", uri)
	}
	name := strings.TrimSpace(link.Name)
	if name == "" {
		name = uri
	}
	text := fmt.Sprintf("Referenced resource %q: %s", name, uri)
	return []message.ContentPart{{Type: message.ContentPartText, Text: text}}
}

// embeddedResourceText extracts the text an embedded resource carries. Chord
// advertises no embedded-context capability, so a compliant client never sends
// one; when a text payload does arrive the model can use it, while a blob has no
// text to give and is rejected instead of being dropped silently.
func embeddedResourceText(block acp.ContentBlockResource) (string, bool) {
	resource := block.Resource.TextResourceContents
	if resource == nil || strings.TrimSpace(resource.Text) == "" {
		return "", false
	}
	if uri := strings.TrimSpace(resource.Uri); uri != "" {
		return fmt.Sprintf("Referenced resource %q:\n%s", uri, resource.Text), true
	}
	return resource.Text, true
}

// filePathFromURI reports the local filesystem path of a file:// URI.
func filePathFromURI(uri string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", false
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		return "", false
	}
	return path, true
}
