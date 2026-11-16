package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"
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
				bare, err := consumeSetupEscape(bufio.NewReader(reader), wait)
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
