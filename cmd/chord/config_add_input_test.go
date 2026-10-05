package main

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConfigAddMenuConsumesOnlySelectedKey(t *testing.T) {
	var out strings.Builder
	reader := bufio.NewReader(strings.NewReader("2next value\n"))
	terminal := &setupTerminal{reader: reader, out: &out}
	value, err := configAddMenu(terminal, "Choices", []string{"first", "second"}, "", "Enter a value")
	if err != nil || value != "second" {
		t.Fatalf("selection: %q, %v", value, err)
	}
	remaining, err := reader.ReadString('\n')
	if err != nil || remaining != "next value\n" {
		t.Fatalf("selection consumed following text: %q, %v", remaining, err)
	}
}

func TestConfigAddMenuManualAndDefault(t *testing.T) {
	for _, input := range []string{"\n", "msecond\n", "92"} {
		terminal := &setupTerminal{reader: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
		value, err := configAddMenu(terminal, "Choices", []string{"first", "second"}, "second", "Enter a value")
		if err != nil || value != "second" {
			t.Fatalf("input %q: %q, %v", input, value, err)
		}
	}
}

func TestConfigAddMenuCancelsImmediately(t *testing.T) {
	for _, input := range []string{"q", "Q", "0", "\x1b", "\x03", "\x04", ""} {
		terminal := &setupTerminal{reader: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
		if _, err := configAddMenu(terminal, "Choices", []string{"first"}, "first", ""); !errors.Is(err, errConfigAddCancelled) {
			t.Fatalf("input %q did not cancel: %v", input, err)
		}
	}
}

func TestConfigAddMenuLargeListDoesNotSelectDigitPrefix(t *testing.T) {
	values := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	terminal := &setupTerminal{reader: bufio.NewReader(strings.NewReader("mj\n")), out: io.Discard}
	value, err := configAddMenu(terminal, "Choices", values, "", "Enter a value")
	if err != nil || value != "j" {
		t.Fatalf("manual selection beyond nine entries: %q, %v", value, err)
	}
}

func TestConfigAddPoolSelectionAndCreation(t *testing.T) {
	for input, expected := range map[string]string{"1": "coding", "2": "default", "mnew-pool\n": "new-pool"} {
		terminal := &setupTerminal{reader: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
		pools := map[string][]string{"default": {}, "coding": {}}
		value, err := chooseConfigAddPool(terminal, pools, "default")
		if err != nil || value != expected {
			t.Fatalf("input %q: pool=%q error=%v", input, value, err)
		}
		if len(pools) != 2 {
			t.Fatal("selection mutated the live pool map before confirmation")
		}
	}
}
