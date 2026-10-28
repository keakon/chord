//go:build !windows

package llm

// codexWindowsVersion is only reached on Windows builds; the native API is not
// available elsewhere, so discovery reports no version and the UA falls back to
// the unknown marker.
func codexWindowsVersion() string { return "" }
