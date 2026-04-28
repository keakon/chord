//go:build unix

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

func readKittyTerminalPixelMetrics() (kittyTerminalMetrics, bool) {
	fd := os.Stdout.Fd()
	ws, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || ws == nil || ws.Col == 0 || ws.Row == 0 {
		return kittyTerminalMetrics{}, false
	}
	metrics := kittyTerminalMetrics{WindowWidthPx: int(ws.Xpixel), WindowHeightPx: int(ws.Ypixel)}
	if metrics.WindowWidthPx > 0 && metrics.WindowHeightPx > 0 {
		metrics.CellWidthPx = max(1, metrics.WindowWidthPx/int(ws.Col))
		metrics.CellHeightPx = max(1, metrics.WindowHeightPx/int(ws.Row))
		metrics.Valid = metrics.CellWidthPx > 0 && metrics.CellHeightPx > 0
	}
	return metrics, metrics.Valid
}
