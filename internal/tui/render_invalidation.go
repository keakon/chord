package tui

type streamRenderInvalidationMode int

const (
	streamRenderInvalidateDefer streamRenderInvalidationMode = iota
	streamRenderInvalidateForce
	streamRenderInvalidateClear
)

func (m *Model) setStreamRenderInvalidation(mode streamRenderInvalidationMode) {
	if m == nil {
		return
	}
	switch mode {
	case streamRenderInvalidateDefer:
		m.streamRenderDeferNext = true
		m.streamRenderDeferred = true
		m.streamRenderForceView = false
	case streamRenderInvalidateForce:
		m.streamRenderDeferNext = false
		m.streamRenderDeferred = false
		m.streamRenderForceView = true
	case streamRenderInvalidateClear:
		m.streamRenderDeferNext = false
		m.streamRenderDeferred = false
		m.streamRenderForceView = false
	}
}
