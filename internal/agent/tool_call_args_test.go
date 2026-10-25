package agent

import (
	"encoding/json"
	"testing"
)

func TestCanonicalToolCallArgsPreservesValuesAndFallback(t *testing.T) {
	for _, input := range []string{`{"b":[true,null],"a":9007199254740993}`, " { \"a\" : 9007199254740993, \"b\" : [true, null] } "} {
		canonical, err := canonicalToolCallArgs(json.RawMessage(input))
		if err != nil || string(canonical) != `{"a":9007199254740993,"b":[true,null]}` {
			t.Fatalf("canonical = %s, err = %v", canonical, err)
		}
		if got := canonicalRepeatedToolCallArgs(json.RawMessage(input)); got != string(canonical) {
			t.Fatalf("loop canonical = %q, want %s", got, canonical)
		}
	}
	for _, input := range []string{"", " ", "{", `{"a":1} trailing`, `[] []`} {
		if _, err := canonicalToolCallArgs(json.RawMessage(input)); err == nil {
			t.Fatalf("invalid input accepted: %q", input)
		}
	}
	for input, want := range map[string]string{
		"": "{}", " ": "{}", " { ": "{", ` [] [] `: "[] []", ` {"a":1} trailing `: `{"a":1} trailing`,
	} {
		if got := canonicalRepeatedToolCallArgs(json.RawMessage(input)); got != want {
			t.Fatalf("loop fallback(%q) = %q, want %q", input, got, want)
		}
	}
}
