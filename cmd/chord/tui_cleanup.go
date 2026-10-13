package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/inputhistory"
	"github.com/keakon/chord/internal/tui"
)

// tuiCleanup is registered before runtime creation and shared by all exit paths.
func tuiCleanup(model *tui.Model, stderr io.Writer) func() {
	cleaned := false
	return func() {
		if cleaned {
			return
		}
		cleaned = true
		if err := model.Close(); err != nil {
			log.Warnf("TUI cleanup failed error=%v", err)
			if historyErr, ok := errors.AsType[*inputhistory.CloseError](err); ok {
				fmt.Fprintf(stderr, "Input history: %d inputs have not been confirmed saved.\n", historyErr.Unconfirmed)
			}
		}
	}
}
