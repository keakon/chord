package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestSetupChoiceFragmentedSequences(t *testing.T) {
	for _, input := range []string{"\x1b[A2", "\x1b[1;2A2", "\x1bOP2", "\x1b[15;2~2"} {
		t.Run(input, func(t *testing.T) {
			reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader(input)))
			value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'1': {}, '2': {}}, '1', "Invalid", true, nil)
			if err != nil || value != "2" {
				t.Fatalf("value=%q err=%v", value, err)
			}
		})
	}
}

func TestSetupChoiceRejectsIncompleteEscape(t *testing.T) {
	for _, prefix := range []string{"\x1b[", "\x1b[20", "\x1bO"} {
		t.Run(prefix, func(t *testing.T) {
			reader := bufio.NewReader(io.MultiReader(strings.NewReader(prefix), strings.NewReader("0~yes\n"+ansi.BracketedPasteEnd+"value\n")))
			value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, func(time.Duration) (bool, error) {
				return false, nil
			})
			if value != "" || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("value=%q error=%v", value, err)
			}
		})
	}
	reader := bufio.NewReader(strings.NewReader("\x1b[20"))
	value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", false, nil)
	if value != "" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("EOF: value=%q error=%v", value, err)
	}
}

func TestSetupEscapeWaitsForFragmentAndBoundsBareEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows readiness checks require a console handle, not an anonymous pipe")
	}
	for _, sequence := range []string{"", "[1;2A"} {
		t.Run(sequence, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			waits := 0
			wait := func(timeout time.Duration) (bool, error) {
				waits++
				if waits == 1 && sequence != "" {
					if _, err := writer.WriteString(sequence); err != nil {
						return false, err
					}
				}
				return waitSetupInput(reader, timeout)
			}
			done := make(chan error, 1)
			go func() {
				_, bare, err := readSetupEscape(bufio.NewReader(reader), wait)
				if bare != (sequence == "") {
					done <- errors.New("incorrect escape classification")
				} else {
					done <- err
				}
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("escape decoding exceeded bound")
			}
		})
	}
}

func TestSetupChoiceAcceptsUppercaseAnswer(t *testing.T) {
	value, err := selectChoiceBySingleKey(bufio.NewReader(strings.NewReader("Y")), io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, nil)
	if err != nil || value != "y" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}

func TestSetupChoicePasteKeepsNextInput(t *testing.T) {
	for _, paste := range []string{"yes\n", " Y\r\nn\n", "y\x1b[20x\n"} {
		t.Run(paste, func(t *testing.T) {
			input := ansi.BracketedPasteStart + paste + ansi.BracketedPasteEnd + "value\n"
			reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader(input)))
			value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, nil)
			if err != nil || value != "y" {
				t.Fatalf("value=%q error=%v", value, err)
			}
			next, err := promptText(&setupTerminal{reader: reader, out: io.Discard}, "Next", "")
			if err != nil || next != "value" {
				t.Fatalf("next=%q error=%v", next, err)
			}
		})
	}
}

func TestSetupChoicePasteWaitsForEnd(t *testing.T) {
	input := io.MultiReader(strings.NewReader(ansi.BracketedPasteStart+"y"), strings.NewReader("es\n"+ansi.BracketedPasteEnd+"value\n"))
	reader := bufio.NewReader(input)
	waits := 0
	value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, func(timeout time.Duration) (bool, error) {
		waits++
		if timeout <= 0 || timeout > setupPasteTimeout {
			t.Fatalf("unbounded wait=%v", timeout)
		}
		return true, nil
	})
	if err != nil || value != "y" || waits == 0 {
		t.Fatalf("value=%q waits=%d error=%v", value, waits, err)
	}
	next, err := reader.ReadString('\n')
	if err != nil || next != "value\n" {
		t.Fatalf("next=%q error=%v", next, err)
	}
}

func TestSetupChoicePasteCancellationAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		wait func(time.Duration) (bool, error)
		want error
	}{
		{"incomplete", "y", nil, io.ErrUnexpectedEOF},
		{"timeout", "y", func(time.Duration) (bool, error) { return false, nil }, os.ErrDeadlineExceeded},
		{"wait error", "y", func(time.Duration) (bool, error) { return false, os.ErrClosed }, os.ErrClosed},
		{"interrupt", "y\x03", nil, errSingleKeyInterrupt},
		{"EOF key", "y\x04", nil, io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(ansi.BracketedPasteStart + tc.body))
			value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, tc.wait)
			if !errors.Is(err, tc.want) || value != "" {
				t.Fatalf("value=%q error=%v, want %v", value, err, tc.want)
			}
		})
	}
}

func TestSetupChoiceInvalidPasteDoesNotAuthorize(t *testing.T) {
	for _, paste := range []string{"", "\r\n", "x1"} {
		reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader(ansi.BracketedPasteStart + paste + ansi.BracketedPasteEnd + "2")))
		value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'1': {}, '2': {}}, 0, "Invalid", false, nil)
		if err != nil || value != "2" {
			t.Fatalf("paste=%q value=%q error=%v", paste, value, err)
		}
	}
}

func TestSetupChoiceSingleKeyKeepsFollowingKeystrokes(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("yvalue\n"))
	value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, nil)
	if err != nil || value != "y" {
		t.Fatalf("value=%q error=%v", value, err)
	}
	next, err := reader.ReadString('\n')
	if err != nil || next != "value\n" {
		t.Fatalf("next=%q error=%v", next, err)
	}
}

func TestSetupChoiceCRLFKeepsNextPrompt(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\r\nvalue\n"))
	value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", true, nil)
	if err != nil || value != "n" {
		t.Fatalf("value=%q error=%v", value, err)
	}
	next, err := reader.ReadString('\n')
	if err != nil || next != "value\n" {
		t.Fatalf("next=%q error=%v", next, err)
	}
}

func TestSetupPasteModeQueryPreservesUserInput(t *testing.T) {
	for _, mode := range []ansi.ModeSetting{ansi.ModeNotRecognized, ansi.ModeSet, ansi.ModeReset, ansi.ModePermanentlySet, ansi.ModePermanentlyReset} {
		reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader("y" + ansi.ReportMode(ansi.ModeBracketedPaste, mode) + "value\n")))
		var out bytes.Buffer
		got, unmarked, err := querySetupPasteMode(reader, &out, nil)
		expected := mode
		if mode == ansi.ModeReset {
			expected = ansi.ModeNotRecognized
		}
		if err != nil || got != expected || !unmarked {
			t.Fatalf("mode=%v got=%v unmarked=%v error=%v", mode, got, unmarked, err)
		}
		if out.String() != ansi.RequestMode(ansi.ModeBracketedPaste) {
			t.Fatalf("query=%q", out.String())
		}
		next, err := reader.ReadString('\n')
		if err != nil || next != "yvalue\n" {
			t.Fatalf("mode=%v next=%q error=%v", mode, next, err)
		}
	}
}

func TestSetupPasteModeQueryEarlyUnmarkedInputFallsBack(t *testing.T) {
	report := ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset)
	for _, input := range []io.Reader{
		iotest.OneByteReader(strings.NewReader("y" + report + "es\n")),
		strings.NewReader(report + "yes\n"),
	} {
		reader := bufio.NewReader(input)
		mode, _, err := querySetupPasteMode(reader, io.Discard, nil)
		if err != nil || mode != ansi.ModeNotRecognized {
			t.Fatalf("early unmarked input must use line mode: mode=%v error=%v", mode, err)
		}
		line, err := reader.ReadString('\n')
		if err != nil || line != "yes\n" {
			t.Fatalf("early input was not preserved: line=%q error=%v", line, err)
		}
	}
}

func TestSetupPasteModeQueryUnknownTerminalKeepsInput(t *testing.T) {
	reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader("yes\n")))
	mode, unmarked, err := querySetupPasteMode(reader, io.Discard, nil)
	if err != nil || mode != ansi.ModeNotRecognized || !unmarked {
		t.Fatalf("mode=%v unmarked=%v error=%v", mode, unmarked, err)
	}
	line, err := reader.ReadString('\n')
	if err != nil || line != "yes\n" {
		t.Fatalf("line=%q error=%v", line, err)
	}
}

func TestSetupPasteModeQueryTracksInputBeforeReport(t *testing.T) {
	report := ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet)
	for _, tc := range []struct {
		name     string
		before   string
		after    string
		unmarked bool
	}{
		{"before report", "y", "value\n", true},
		{"after report", "", "yvalue\n", false},
		{"paste", ansi.BracketedPasteStart + "yes\n" + ansi.BracketedPasteEnd, "value\n", false},
		{"report inside paste", ansi.BracketedPasteStart + "y", "es\n" + ansi.BracketedPasteEnd + "value\n", false},
		{"unmarked after paste", ansi.BracketedPasteStart + "yes\n" + ansi.BracketedPasteEnd + "n", "value\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.before + report + tc.after))
			mode, unmarked, err := querySetupPasteMode(reader, io.Discard, nil)
			if err != nil || mode != ansi.ModeSet || unmarked != tc.unmarked {
				t.Fatalf("mode=%v unmarked=%v error=%v", mode, unmarked, err)
			}
			input, err := io.ReadAll(reader)
			if err != nil || string(input) != tc.before+tc.after {
				t.Fatalf("input=%q error=%v", input, err)
			}
		})
	}
}

func TestSetupPasteModeQueryKeepsBufferedPasteVisible(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(ansi.BracketedPasteStart + "y" + ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet) + "es\n" + ansi.BracketedPasteEnd + "value\n"))
	mode, unmarked, err := querySetupPasteMode(reader, io.Discard, nil)
	if err != nil || mode != ansi.ModeSet || unmarked {
		t.Fatalf("mode=%v unmarked=%v error=%v", mode, unmarked, err)
	}
	value, err := selectChoiceBySingleKey(reader, io.Discard, "", map[byte]struct{}{'y': {}, 'n': {}}, 'n', "Invalid", false, func(time.Duration) (bool, error) {
		return false, nil
	})
	if err != nil || value != "y" {
		t.Fatalf("value=%q error=%v", value, err)
	}
	next, err := reader.ReadString('\n')
	if err != nil || next != "value\n" {
		t.Fatalf("next=%q error=%v", next, err)
	}
}
