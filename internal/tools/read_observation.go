package tools

import (
	"context"
	"strings"
	"sync"
)

// ReadObservation identifies the exact file bytes used to produce one Read
// result. Path is the resolved file path and SHA256 hashes the raw on-disk
// bytes from that same read.
type ReadObservation struct {
	Path   string
	SHA256 string
}

// ReadObservationSink receives exact-version metadata from ReadTool without
// exposing it in model-visible tool output.
type ReadObservationSink interface {
	SetReadObservation(ReadObservation)
}

// ReadObservationCollector is scoped to one tool execution through context.
type ReadObservationCollector struct {
	mu          sync.Mutex
	observation ReadObservation
}

func (c *ReadObservationCollector) SetReadObservation(observation ReadObservation) {
	if c == nil {
		return
	}
	observation.Path = strings.TrimSpace(observation.Path)
	observation.SHA256 = strings.TrimSpace(observation.SHA256)
	if observation.Path == "" || observation.SHA256 == "" {
		return
	}
	c.mu.Lock()
	c.observation = observation
	c.mu.Unlock()
}

func (c *ReadObservationCollector) Observation() (ReadObservation, bool) {
	if c == nil {
		return ReadObservation{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	observation := c.observation
	return observation, observation.Path != "" && observation.SHA256 != ""
}

// WithReadObservationSink returns a context carrying sink. A nil sink leaves
// the context unchanged.
func WithReadObservationSink(ctx context.Context, sink ReadObservationSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, readObservationSinkKey, sink)
}

func readObservationSinkFromContext(ctx context.Context) (ReadObservationSink, bool) {
	if ctx == nil {
		return nil, false
	}
	sink, ok := ctx.Value(readObservationSinkKey).(ReadObservationSink)
	return sink, ok
}
