package tui

import (
	tea "github.com/keakon/bubbletea/v2"
)

// tickCmd is the package's single timer seam. Tests that drive commands
// synchronously (runCmdTree, flattenCmdMsgs, collectCommandMessages) replace
// it with an immediate stub so they do not block on real UI delays such as
// toast lifetimes, chord timeouts and background tickers.
var tickCmd = tea.Tick
