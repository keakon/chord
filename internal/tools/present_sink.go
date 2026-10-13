package tools

import (
	"context"
	"sync"

	"github.com/keakon/chord/internal/message"
)

type deliveryContextKey struct{}

// DeliveryCollector belongs to one direct invocation, never to a registry.
type DeliveryCollector struct {
	mu    sync.Mutex
	files []message.DeliveredFile
}

func (c *DeliveryCollector) Add(files []message.DeliveredFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files = append(c.files, message.CloneDeliveredFiles(files)...)
}
func (c *DeliveryCollector) Drain() []message.DeliveredFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	files := c.files
	c.files = nil
	return files
}
func WithDeliveryCollector(ctx context.Context, c *DeliveryCollector) context.Context {
	return context.WithValue(ctx, deliveryContextKey{}, c)
}

type approvedDeliveryTargetsKey struct{}

func WithApprovedDeliveryTargets(ctx context.Context, locators []message.FileLocator) context.Context {
	return context.WithValue(ctx, approvedDeliveryTargetsKey{}, locators)
}
