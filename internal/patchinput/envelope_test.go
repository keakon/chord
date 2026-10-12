package patchinput

import (
	"encoding/json"
	"testing"
)

func TestUnwrapEnvelope(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"
	inputEnvelope, err := json.Marshal(map[string]string{"input": patch})
	if err != nil {
		t.Fatal(err)
	}
	patchEnvelope, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := json.Marshal(map[string]string{"input": string(inputEnvelope)})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare patch text untouched", patch, patch},
		{"input envelope unwrapped", string(inputEnvelope), patch},
		{"patch envelope unwrapped", string(patchEnvelope), patch},
		{"nested envelope unwrapped", string(nested), patch},
		{"non-patch JSON untouched", `{"input":"not a patch"}`, `{"input":"not a patch"}`},
		{"truncated JSON untouched", `{"input":"*** Begin Patch`, `{"input":"*** Begin Patch`},
		{"non-string value untouched", `{"input":{"patch":"*** Begin Patch"}}`, `{"input":{"patch":"*** Begin Patch"}}`},
		{"canonical field takes precedence", `{"patch":"*** Update File: a.txt","input":"*** Delete File: b.txt"}`, "*** Update File: a.txt"},
		{"invalid canonical field does not fall back", `{"patch":null,"input":"*** Delete File: b.txt"}`, `{"patch":null,"input":"*** Delete File: b.txt"}`},
		{"unrelated nested JSON untouched", `{"input":"{\"patch\":123}"}`, `{"input":"{\"patch\":123}"}`},
		{"unknown large number ignored", `{"input":"*** Begin Patch","extra":1e1000}`, "*** Begin Patch"},
		{"unescaped control character rejected", "{\"input\":\"*** Begin Patch\n\"}", "{\"input\":\"*** Begin Patch\n\"}"},
		{"empty text untouched", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnwrapEnvelope(tt.in); got != tt.want {
				t.Fatalf("UnwrapEnvelope(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got := UnwrapEnvelope(UnwrapEnvelope(tt.in)); got != tt.want {
				t.Fatalf("repeated normalization = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnwrapEnvelopeLayerLimitIsAtomic(t *testing.T) {
	patch := "*** Begin Patch\n*** Delete File: file.txt\n*** End Patch"
	text := patch
	for layer := 1; layer <= MaxEnvelopeLayers+1; layer++ {
		raw, err := json.Marshal(map[string]string{"input": text})
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
		want := patch
		if layer > MaxEnvelopeLayers {
			want = text
		}
		if got := UnwrapEnvelope(text); got != want {
			t.Fatalf("layer %d: got %q, want %q", layer, got, want)
		}
		if got := UnwrapEnvelope(UnwrapEnvelope(text)); got != want {
			t.Fatalf("layer %d: repeated normalization changed the result", layer)
		}
	}
}
