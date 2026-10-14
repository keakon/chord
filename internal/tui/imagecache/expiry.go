package imagecache

import "time"

const PayloadIdleTTL = 30 * time.Second

// ReleaseIdle retains dimensions but drops reconstructible byte slices
// and strings. TryLock skips active readers/decoders so maintenance never waits
// for image I/O on the main loop. Preview construction can request another
// entry while holding its own lock, so never block while holding the store lock.
func (s *Store) ReleaseIdle(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.residentBytes.Load() == 0 || !e.mu.TryLock() {
			continue
		}
		if now.Sub(e.lastPayloadAccess) >= PayloadIdleTTL {
			e.rawData, e.pngData, e.base64PNG = nil, nil, ""
			e.rawLoaded, e.pngLoaded, e.base64Loaded = false, false, false
			e.rawErr, e.pngErr, e.base64Err = nil, nil, nil
			e.pngWidth, e.pngHeight = 0, 0
			e.residentBytes.Store(0)
		}
		e.mu.Unlock()
	}
}
