//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type setupTestPTYReader struct{ file *os.File }

func (r setupTestPTYReader) Read(p []byte) (int, error) {
	ready, err := waitSetupInput(r.file, 5*time.Second)
	if err != nil {
		return 0, err
	}
	if !ready {
		return 0, os.ErrDeadlineExceeded
	}
	return r.file.Read(p[:min(len(p), 1)])
}

func readSetupTestPTYUntil(t *testing.T, master *os.File, output *bytes.Buffer, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var data [1024]byte
	for !strings.Contains(output.String(), marker) {
		ready, err := waitSetupInput(master, time.Until(deadline))
		if err != nil || !ready {
			t.Fatalf("wait for %q: ready=%v error=%v output=%q", marker, ready, err, output.String())
		}
		n, err := master.Read(data[:])
		if err != nil {
			t.Fatalf("read PTY: %v output=%q", err, output.String())
		}
		output.Write(data[:n])
	}
}

func writeSetupTestPTY(t *testing.T, master *os.File, text string) {
	t.Helper()
	if _, err := io.WriteString(master, text); err != nil {
		t.Fatal(err)
	}
}

func TestSetupTerminalChoiceAndPaste(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   ansi.ModeSetting
		early  string
		inputs []string
	}{
		{"single key", ansi.ModeReset, "", []string{"y"}},
		{"segmented paste", ansi.ModeReset, "", []string{ansi.BracketedPasteStart, "Y", "es\nn\n", ansi.BracketedPasteEnd}},
		{"existing paste mode", ansi.ModeSet, "", []string{ansi.BracketedPasteStart, "y\n", ansi.BracketedPasteEnd}},
		{"unsupported line fallback", ansi.ModeNotRecognized, "", []string{"y", "es\n"}},
		{"early unmarked input", ansi.ModeReset, "y", []string{"es\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave, err := openSetupTestPTY()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			base, err := setupTestTerminalSettings(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				termIO := &setupTerminal{in: slave, out: slave, reader: bufio.NewReader(setupTestPTYReader{file: slave})}
				selected, choiceErr := promptYesNo(termIO, "PTY_CHOICE: ", false)
				if choiceErr != nil || !selected {
					done <- fmt.Errorf("selected=%v: %w", selected, choiceErr)
					return
				}
				next, promptErr := promptText(termIO, "PTY_NEXT", "")
				if promptErr != nil {
					done <- promptErr
					return
				}
				if next != "value" {
					done <- fmt.Errorf("paste reached next prompt: %q", next)
					return
				}
				done <- nil
			}()
			var output bytes.Buffer
			readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
			writeSetupTestPTY(t, master, tc.early+ansi.ReportMode(ansi.ModeBracketedPaste, tc.mode))
			if tc.mode == ansi.ModeReset && tc.early == "" {
				readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
				writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet))
			}
			readSetupTestPTYUntil(t, master, &output, "PTY_CHOICE: ")
			for _, input := range tc.inputs {
				writeSetupTestPTY(t, master, input)
			}
			readSetupTestPTYUntil(t, master, &output, "PTY_NEXT: ")
			writeSetupTestPTY(t, master, "value\n")
			select {
			case promptErr := <-done:
				if promptErr != nil {
					t.Fatal(promptErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("terminal prompt did not finish")
			}
			restored, err := setupTestTerminalSettings(slave.Fd())
			if err != nil || !reflect.DeepEqual(base, restored) {
				t.Fatalf("terminal was not restored: state=%+v error=%v", restored, err)
			}
			enabled := strings.Contains(output.String(), ansi.SetModeBracketedPaste)
			reset := strings.Contains(output.String(), ansi.ResetModeBracketedPaste)
			if enabled != (tc.mode == ansi.ModeReset && tc.early == "") || reset != enabled {
				t.Fatalf("paste mode not preserved: output=%q", output.String())
			}
		})
	}
}

func TestSetupTerminalRestoresOnError(t *testing.T) {
	master, slave, err := openSetupTestPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	base, err := setupTestTerminalSettings(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		termIO := &setupTerminal{in: slave, out: slave, reader: bufio.NewReader(setupTestPTYReader{file: slave})}
		_, handled, promptErr := withSingleKeyTerminal(termIO, func(*bufio.Reader, io.Writer) (string, error) {
			return "", os.ErrClosed
		})
		if !handled || !errors.Is(promptErr, os.ErrClosed) {
			done <- fmt.Errorf("handled=%v error=%v", handled, promptErr)
		} else {
			done <- nil
		}
	}()
	var output bytes.Buffer
	readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset))
	readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
	writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet))
	readSetupTestPTYUntil(t, master, &output, ansi.ResetModeBracketedPaste)
	select {
	case promptErr := <-done:
		if promptErr != nil {
			t.Fatal(promptErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal error path did not finish")
	}
	restored, err := setupTestTerminalSettings(slave.Fd())
	if err != nil || !reflect.DeepEqual(base, restored) {
		t.Fatalf("terminal was not restored: before=%+v after=%+v error=%v", base, restored, err)
	}
}

func TestSetupTerminalRawRestoreInvariant(t *testing.T) {
	master, slave, err := openSetupTestPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	base, err := setupTestTerminalSettings(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	captured, err := term.MakeRaw(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if err := term.Restore(slave.Fd(), captured); err != nil {
		t.Fatal(err)
	}
	restored, err := setupTestTerminalSettings(slave.Fd())
	if err != nil || !reflect.DeepEqual(base, restored) {
		t.Fatalf("direct restore: before=%+v captured=%+v after=%+v error=%v", base, captured, restored, err)
	}
}

func TestSetupTerminalInterruptHelper(t *testing.T) {
	path := os.Getenv("CHORD_SETUP_TEST_PTY")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	termIO := &setupTerminal{in: file, out: file, reader: bufio.NewReader(setupTestPTYReader{file: file})}
	armSetupInterrupt(termIO)
	defer termIO.interrupt.stop()
	if _, err := promptYesNo(termIO, "PTY_INTERRUPT: ", false); err != nil {
		t.Fatal(err)
	}
	t.Fatal("interrupt did not exit")
}

func TestSetupTerminalInterruptRestoresState(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name               string
		signalProcess      bool
		duringConfirmation bool
	}{
		{"key at choice", false, false},
		{"signal at choice", true, false},
		{"key during confirmation", false, true},
		{"signal during confirmation", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave, err := openSetupTestPTY()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			base, err := setupTestTerminalSettings(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSetupTerminalInterruptHelper$")
			cmd.Env = append(os.Environ(), "CHORD_SETUP_TEST_PTY="+slave.Name())
			var processOutput bytes.Buffer
			cmd.Stdout, cmd.Stderr = &processOutput, &processOutput
			if startErr := cmd.Start(); startErr != nil {
				t.Fatal(startErr)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			var output bytes.Buffer
			readSetupTestPTYUntil(t, master, &output, ansi.RequestMode(ansi.ModeBracketedPaste))
			writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeReset))
			readSetupTestPTYUntil(t, master, &output, ansi.SetModeBracketedPaste+ansi.RequestMode(ansi.ModeBracketedPaste))
			if !tc.duringConfirmation {
				writeSetupTestPTY(t, master, ansi.ReportMode(ansi.ModeBracketedPaste, ansi.ModeSet))
				readSetupTestPTYUntil(t, master, &output, "PTY_INTERRUPT: ")
			}
			if tc.signalProcess {
				if signalErr := cmd.Process.Signal(syscall.SIGINT); signalErr != nil {
					t.Fatal(signalErr)
				}
			} else {
				writeSetupTestPTY(t, master, "\x03")
			}
			err = cmd.Wait()
			exitErr, ok := errors.AsType[*exec.ExitError](err)
			if !ok || exitErr.ExitCode() != 130 {
				t.Fatalf("interrupt error=%v output=%q", err, processOutput.String())
			}
			readSetupTestPTYUntil(t, master, &output, ansi.ResetModeBracketedPaste)
			restored, err := setupTestTerminalSettings(slave.Fd())
			if err != nil || !reflect.DeepEqual(base, restored) {
				t.Fatalf("terminal was not restored after interrupt: state=%+v error=%v", restored, err)
			}
		})
	}
}
