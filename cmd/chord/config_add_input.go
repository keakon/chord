package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/term"
)

// readConfigAddKey restores terminal state before the next text prompt or
// error is printed. Sharing the buffered reader also preserves pasted input.
func readConfigAddKey(t *setupTerminal) (key byte, err error) {
	if t.in != nil {
		state, rawErr := term.MakeRaw(t.in.Fd())
		if rawErr != nil {
			return 0, fmt.Errorf("enable immediate selection: %w", rawErr)
		}
		defer func() {
			if restoreErr := term.Restore(t.in.Fd(), state); restoreErr != nil {
				err = fmt.Errorf("restore selection terminal: %w", restoreErr)
			}
		}()
	}
	return t.reader.ReadByte()
}

func configAddKey(t *setupTerminal, label, allowed string, defaultKey byte) (byte, error) {
	for {
		if _, err := fmt.Fprint(t.out, label+": "); err != nil {
			return 0, err
		}
		key, err := readConfigAddKey(t)
		if errors.Is(err, io.EOF) || (err == nil && (key == 27 || key == 3 || key == 4 || key == 'q' || key == 'Q')) {
			fmt.Fprintln(t.out)
			return 0, errConfigAddCancelled
		}
		if err != nil {
			return 0, err
		}
		if key == '\r' || key == '\n' {
			key = defaultKey
		}
		if key >= 'A' && key <= 'Z' {
			key += 'a' - 'A'
		}
		if key != 0 && strings.ContainsRune(allowed, rune(key)) {
			fmt.Fprintf(t.out, "%c\n", key)
			return key, nil
		}
		fmt.Fprintln(t.out, "Press one of the listed keys, or Esc to cancel.")
	}
}

func configAddYesNo(t *setupTerminal, label string, defaultYes bool) (bool, error) {
	defaultKey := byte('n')
	if defaultYes {
		defaultKey = 'y'
	}
	key, err := configAddKey(t, fmt.Sprintf("%s (y/n) [%c]", label, defaultKey), "yn", defaultKey)
	return key == 'y', err
}

// configAddMenu gives single-digit choices immediate selection. Longer lists
// use m to enter a full value rather than ambiguously accepting a digit prefix.
func configAddMenu(t *setupTerminal, label string, values []string, defaultValue, manualLabel string) (string, error) {
	fmt.Fprintln(t.out, label+":")
	var allowed strings.Builder
	allowed.WriteByte('0')
	for i, value := range values {
		if i < 9 {
			fmt.Fprintf(t.out, "  %d) %s\n", i+1, value)
			allowed.WriteByte(byte('1' + i))
		} else {
			fmt.Fprintf(t.out, "     %s\n", value)
		}
	}
	if manualLabel != "" {
		fmt.Fprintf(t.out, "  m) %s\n", manualLabel)
		allowed.WriteByte('m')
	}
	defaultKey := byte(0)
	for i, value := range values {
		if value == defaultValue && i < 9 {
			defaultKey = byte('1' + i)
		}
	}
	prompt := "Press a number (no Enter), m for text input, or Esc/0 to cancel"
	if manualLabel == "" {
		prompt = "Press a number (no Enter), or Esc/0 to cancel"
	}
	if defaultKey != 0 {
		prompt += fmt.Sprintf(" [%c]", defaultKey)
	}
	key, err := configAddKey(t, prompt, allowed.String(), defaultKey)
	if err != nil {
		return "", err
	}
	if key == '0' {
		return "", errConfigAddCancelled
	}
	if key == 'm' {
		return configAddPrompt(t, manualLabel, "")
	}
	return values[int(key-'1')], nil
}
