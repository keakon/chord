package mediacache

import "testing"

func BenchmarkMediaEncodingMixedWire(b *testing.B) {
	previous := imageEncodingCache
	c := newImageLRUCache(64, imageEncodingCacheBudget)
	imageEncodingCache = c
	b.Cleanup(func() {
		c.mu.Lock()
		if c.idleTimer != nil {
			c.idleTimer.Stop()
		}
		c.mu.Unlock()
		imageEncodingCache = previous
	})
	data := make([]byte, 6<<20)
	Digest(data)
	EncodeBase64(data)
	DataURL("application/pdf", data)
	b.ReportAllocs()
	for b.Loop() {
		EncodeBase64(data)
		DataURL("application/pdf", data)
	}
	b.StopTimer()
	encoded, dataURL := ResidentBytes()
	b.ReportMetric(float64(encoded+dataURL), "cache-B")
}
