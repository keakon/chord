package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
)

type toolCallResult struct {
	Content           []toolCallContent `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

type toolCallContent struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	Name     string            `json:"name,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Resource *toolCallResource `json:"resource,omitempty"`
}

type toolCallResource struct {
	URI      string  `json:"uri"`
	MimeType string  `json:"mimeType,omitempty"`
	Text     *string `json:"text,omitempty"`
	Blob     *string `json:"blob,omitempty"`
}

// normalizeToolCallResult projects MCP content onto the existing text/image
// tool result path. Resource links are references; they are never fetched here.
func (c *Client) normalizeToolCallResult(ctx context.Context, toolName string, result toolCallResult) (string, []message.ContentPart, error) {
	var text []string
	var images []message.ContentPart
	var imageFailures []error
	appendImage := func(data, mime string) {
		if result.IsError {
			text = append(text, "[image omitted from failed tool result]")
			return
		}
		var raw []byte
		var err error
		if data == "" {
			err = fmt.Errorf("empty image data")
		} else {
			raw, err = base64.StdEncoding.DecodeString(data)
		}
		if err == nil {
			raw, mime, err = imageutil.NormalizeImageBytes(ctx, raw, mime)
		}
		if err != nil {
			log.Warnf("mcp tools/call %s/%s: omitting image block error=%v", c.name, toolName, err)
			imageFailures = append(imageFailures, err)
			return
		}
		images = append(images, message.ContentPart{Type: "image", MimeType: mime, Data: raw})
	}
	if len(result.StructuredContent) > 0 {
		raw := bytes.TrimSpace(result.StructuredContent)
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			text = append(text, "[MCP structured content warning] structuredContent is not valid JSON; usable content was preserved")
		} else {
			duplicated := false
			for _, block := range result.Content {
				if block.Type != "text" {
					continue
				}
				var candidate bytes.Buffer
				if json.Compact(&candidate, []byte(block.Text)) == nil && bytes.Equal(candidate.Bytes(), compact.Bytes()) {
					duplicated = true
					break
				}
			}
			if !duplicated {
				text = append(text, compact.String())
			}
		}
	}
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				text = append(text, block.Text)
			}
		case "image":
			appendImage(block.Data, block.MimeType)
		case "resource":
			r := block.Resource
			if r == nil {
				return "", nil, fmt.Errorf("mcp tools/call: missing embedded resource")
			}
			if r.URI == "" {
				return "", nil, fmt.Errorf("mcp tools/call: embedded resource requires uri")
			}
			switch {
			case r.Text != nil:
				if *r.Text != "" {
					text = append(text, *r.Text)
				}
			case r.Blob == nil:
				return "", nil, fmt.Errorf("mcp tools/call: embedded resource must contain text or blob")
			case strings.HasPrefix(r.MimeType, "image/"):
				appendImage(*r.Blob, r.MimeType)
			default:
				mime := r.MimeType
				if mime == "" {
					mime = "unknown type"
				}
				text = append(text, fmt.Sprintf("[binary resource %s (%s) omitted]", r.URI, mime))
			}
		case "resource_link":
			if block.Name == "" || block.URI == "" {
				return "", nil, fmt.Errorf("mcp tools/call: resource link requires name and uri")
			}
			text = append(text, block.Name+": "+block.URI)
		case "audio":
			text = append(text, fmt.Sprintf("[audio %s omitted]", block.MimeType))
		case "":
			return "", nil, fmt.Errorf("mcp tools/call: missing content type")
		default:
			text = append(text, fmt.Sprintf("[unsupported MCP content %s]", block.Type))
		}
	}
	if len(imageFailures) > 0 {
		text = append(text, fmt.Sprintf("%d image attachment(s) in this result could not be read and were omitted: %v", len(imageFailures), imageFailures[0]))
	}
	joined := strings.Join(text, "\n")
	if result.IsError {
		if joined == "" {
			joined = "server returned isError without details"
		}
		return "", nil, fmt.Errorf("mcp tool error: %s", joined)
	}
	return joined, images, nil
}
