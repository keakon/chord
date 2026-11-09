//go:build unix && !darwin && !linux

package buildcache

// An unavailable boot identity keeps ownership records protected.
func systemBootID() string { return "" }
