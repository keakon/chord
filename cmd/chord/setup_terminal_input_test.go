//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type setupTestPasteEnableWriter struct {
	master, slave *os.File
	before        string
	err           error
}

func (w setupTestPasteEnableWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), ansi.SetModeBracketedPaste) {
		if w.err != nil {
			return 0, w.err
		}
		if _, err := io.WriteString(w.master, w.before); err != nil {
			return 0, err
		}
	}
	return w.slave.Write(p)
}

func TestSetupTerminalInputBeforeEnableUsesCompleteLine(t *testing.T) {
	master, slave, openErr := openSetupTestPTY()
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer master.Close()
	defer slave.Close()
	base, stateErr := setupTestTerminalSettings(slave.Fd())
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	done := make(chan error, 1)
	go func() {
		termIO := &setupTerminal{
			in: slave, out: setupTestPasteEnableWriter{master: master, slave: slave, before: "y"},
			reader: bufio.NewReader(slave),
		}
		selected, err := promptYesNo(termIO, "PTY_CHOICE: ", false)
		if err != nil || !selected {
			done <- fmt.Errorf("selected=%v error=%v", selected, err)
			return
		}
		next, err := promptText(termIO, "PTY_NEXT", "")
		if err != nil || next != "value" {
			done <- fmt.Errorf("next=%q error=%v", next, err)
			return
		}
		done <- nil
	}()
	var output bytes.Buffer
	readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset))
	readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet))
	readSetupTestPTYUntil(t, master, &output, "PTY_CHOICE: ")
	if !strings.Contains(output.String(), ansi.ResetModeBracketedPaste) {
		t.Fatalf("line fallback kept paste mode enabled: output=%q", output.String())
	}
	restored, err := setupTestTerminalSettings(slave.Fd())
	if err != nil || !reflect.DeepEqual(base, restored) {
		t.Fatalf("line fallback kept raw mode: state=%+v error=%v", restored, err)
	}
	writeSetupTestPTY(t, master, "es\nvalue\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("early input did not finish")
	}
}

func TestSetupTerminalPasteDuringModeQuery(t *testing.T) {
	for _, initial := range []ansi.ModeSetting{ansi.ModeSet, ansi.ModeReset} {
		t.Run(fmt.Sprintf("initial=%v", initial), func(t *testing.T) {
			master, slave, openErr := openSetupTestPTY()
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer master.Close()
			defer slave.Close()
			base, stateErr := setupTestTerminalSettings(slave.Fd())
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			done := make(chan error, 1)
			go func() {
				termIO := &setupTerminal{in: slave, out: slave, reader: bufio.NewReader(slave)}
				selected, err := promptYesNo(termIO, "PTY_CHOICE: ", false)
				if err != nil || !selected {
					done <- fmt.Errorf("selected=%v error=%v", selected, err)
					return
				}
				next, err := promptText(termIO, "PTY_NEXT", "")
				if err != nil || next != "value" {
					done <- fmt.Errorf("next=%q error=%v", next, err)
					return
				}
				done <- nil
			}()
			var output bytes.Buffer
			readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
			if initial == ansi.ModeReset {
				writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, initial))
				readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
			}
			writeSetupTestPTY(t, master, ansi.BracketedPasteStart+"y"+ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet)+"es\n"+ansi.BracketedPasteEnd+"value\n")
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("complete buffered paste did not finish")
			}
			readSetupTestPTYUntil(t, master, &output, "PTY_NEXT: ")
			restored, err := setupTestTerminalSettings(slave.Fd())
			if err != nil || !reflect.DeepEqual(base, restored) {
				t.Fatalf("terminal was not restored: state=%+v error=%v", restored, err)
			}
			enabled := strings.Contains(output.String(), ansi.SetModeBracketedPaste)
			reset := strings.Contains(output.String(), ansi.ResetModeBracketedPaste)
			if enabled != (initial == ansi.ModeReset) || reset != enabled {
				t.Fatalf("paste mode not preserved: output=%q", output.String())
			}
		})
	}
}

func TestSetupTerminalIncompletePasteStartRestoresState(t *testing.T) {
	master, slave, openErr := openSetupTestPTY()
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer master.Close()
	defer slave.Close()
	base, stateErr := setupTestTerminalSettings(slave.Fd())
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	done := make(chan error, 1)
	go func() {
		termIO := &setupTerminal{in: slave, out: slave, reader: bufio.NewReader(slave)}
		value, _, err := promptChoiceSingleKey(termIO, "PTY_CHOICE: ", []string{"y", "n"}, "n", "Invalid")
		if value != "" {
			done <- fmt.Errorf("incomplete paste submitted %q", value)
		} else {
			done <- err
		}
	}()
	var output bytes.Buffer
	readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset))
	readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet))
	readSetupTestPTYUntil(t, master, &output, "PTY_CHOICE: ")
	writeSetupTestPTY(t, master, "\x1b[20")
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incomplete paste start exceeded its bound")
	}
	readSetupTestPTYUntil(t, master, &output, ansi.ResetModeBracketedPaste)
	restored, err := setupTestTerminalSettings(slave.Fd())
	if err != nil || !reflect.DeepEqual(base, restored) {
		t.Fatalf("terminal was not restored: state=%+v error=%v", restored, err)
	}
}

func TestSetupTerminalPasteModeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   string
		handled bool
		err     error
	}{
		{"enabled", ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet), true, nil},
		{"still disabled", ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset), false, nil},
		{"unsupported", ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeNotRecognized), false, nil},
		{"no report", "", false, nil},
		{"incomplete report", "\x1b[?2004", true, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave, openErr := openSetupTestPTY()
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer master.Close()
			defer slave.Close()
			base, stateErr := setupTestTerminalSettings(slave.Fd())
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			done := make(chan error, 1)
			go func() {
				termIO := &setupTerminal{in: slave, out: slave, reader: bufio.NewReader(slave)}
				called := false
				value, handled, err := withSingleKeyTerminal(termIO, func(*bufio.Reader, io.Writer) (string, error) {
					called = true
					return "y", nil
				})
				wantValue := ""
				if tc.handled && tc.err == nil {
					wantValue = "y"
				}
				if handled != tc.handled || value != wantValue || called != (wantValue != "") || !errors.Is(err, tc.err) {
					done <- fmt.Errorf("value=%q handled=%v called=%v error=%v", value, handled, called, err)
				} else {
					done <- nil
				}
			}()
			var output bytes.Buffer
			readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
			writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset))
			readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
			writeSetupTestPTY(t, master, tc.reply)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("paste mode confirmation exceeded its bound")
			}
			readSetupTestPTYUntil(t, master, &output, ansi.ResetModeBracketedPaste)
			restored, err := setupTestTerminalSettings(slave.Fd())
			if err != nil || !reflect.DeepEqual(base, restored) {
				t.Fatalf("terminal was not restored: state=%+v error=%v", restored, err)
			}
		})
	}
}

func TestSetupTerminalPasteEnableWriteErrorRestoresState(t *testing.T) {
	master, slave, openErr := openSetupTestPTY()
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer master.Close()
	defer slave.Close()
	base, stateErr := setupTestTerminalSettings(slave.Fd())
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	done := make(chan error, 1)
	go func() {
		termIO := &setupTerminal{
			in: slave, out: setupTestPasteEnableWriter{master: master, slave: slave, err: os.ErrClosed},
			reader: bufio.NewReader(slave),
		}
		called := false
		_, handled, err := withSingleKeyTerminal(termIO, func(*bufio.Reader, io.Writer) (string, error) {
			called = true
			return "y", nil
		})
		if called || !handled || !errors.Is(err, os.ErrClosed) {
			done <- fmt.Errorf("called=%v handled=%v error=%v", called, handled, err)
		} else {
			done <- nil
		}
	}()
	var output bytes.Buffer
	readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset))
	readSetupTestPTYUntil(t, master, &output, ansi.ResetModeBracketedPaste)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("enable error did not finish")
	}
	restored, err := setupTestTerminalSettings(slave.Fd())
	if err != nil || !reflect.DeepEqual(base, restored) {
		t.Fatalf("terminal was not restored: state=%+v error=%v", restored, err)
	}
}
