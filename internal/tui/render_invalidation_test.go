package tui

import "testing"

func TestStreamRenderInvalidationModes(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	m.setStreamRenderInvalidation(streamRenderInvalidateDefer)
	if !m.streamRenderDeferNext || !m.streamRenderDeferred || m.streamRenderForceView {
		t.Fatalf("defer mode = force:%v deferred:%v next:%v", m.streamRenderForceView, m.streamRenderDeferred, m.streamRenderDeferNext)
	}

	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
	if m.streamRenderDeferNext || m.streamRenderDeferred || !m.streamRenderForceView {
		t.Fatalf("force mode = force:%v deferred:%v next:%v", m.streamRenderForceView, m.streamRenderDeferred, m.streamRenderDeferNext)
	}

	m.setStreamRenderInvalidation(streamRenderInvalidateClear)
	if m.streamRenderDeferNext || m.streamRenderDeferred || m.streamRenderForceView {
		t.Fatalf("clear mode = force:%v deferred:%v next:%v", m.streamRenderForceView, m.streamRenderDeferred, m.streamRenderDeferNext)
	}
}
