package tools

import (
	"context"
	"sync"

	"github.com/keakon/chord/internal/message"
)

// ImageSink receives image (and other binary, e.g. PDF) content parts produced
// by a tool during its execution. The runtime injects a fresh sink per tool
// call via context, then attaches collected parts to that tool's result message.
//
// This is the shared output channel for both the native ViewImage tool and MCP
// tools that return non-text content, avoiding any change to the Tool.Execute
// signature.
type ImageSink interface {
	AddImage(part message.ContentPart)
}

// ImageCollector is the default ImageSink implementation. A fresh collector is
// created per tool execution and injected via context, so concurrent and
// speculative tool runs never share state (no "last result" race).
type ImageCollector struct {
	mu    sync.Mutex
	parts []message.ContentPart
}

// AddImage appends a content part to the collector.
func (c *ImageCollector) AddImage(part message.ContentPart) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parts = append(c.parts, part)
}

// Drain returns the collected parts and clears the collector.
func (c *ImageCollector) Drain() []message.ContentPart {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.parts) == 0 {
		return nil
	}
	out := c.parts
	c.parts = nil
	return out
}

// WithImageSink returns a context carrying the given image sink. A nil sink
// leaves the context unchanged.
func WithImageSink(ctx context.Context, sink ImageSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, imageSinkKey, sink)
}

// ImageSinkFromContext extracts the image sink from the context, or returns
// (nil, false) if absent.
func ImageSinkFromContext(ctx context.Context) (ImageSink, bool) {
	sink, ok := ctx.Value(imageSinkKey).(ImageSink)
	return sink, ok
}
