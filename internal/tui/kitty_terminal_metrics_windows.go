//go:build windows

package tui

func readKittyTerminalPixelMetrics() (kittyTerminalMetrics, bool) {
	return kittyTerminalMetrics{}, false
}
