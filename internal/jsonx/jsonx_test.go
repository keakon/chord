package jsonx

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"testing"
)

type sample struct {
	Name   string          `json:"name"`
	Count  int             `json:"count,omitzero"`
	Tags   []string        `json:"tags"`
	Nil    []string        `json:"nil"`
	Attrs  map[string]any  `json:"attrs"`
	Raw    json.RawMessage `json:"raw,omitzero"`
	Hidden string          `json:"-"`
}

// The canonical option set must reproduce the v1 API's bytes for ordinary
// values: sorted map keys, nil containers as null, omitzero semantics, and
// verbatim RawMessage passthrough for duplicate members.
func TestMarshalMatchesV1Output(t *testing.T) {
	value := sample{
		Name:  "main",
		Attrs: map[string]any{"z": 1, "a": []int{3, 1, 2}},
		Raw:   json.RawMessage(`{"k":[1,{"x":1,"x":2}],"note":"ok"}`),
	}
	v1, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("v1 marshal: %v", err)
	}
	got, err := Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != string(v1) {
		t.Fatalf("output mismatch:\n v1=%s\n v2=%s", v1, got)
	}
}

// HTML escaping is deliberately not performed: v1 escaped <, > and &, the
// canonical encoder does not.
func TestMarshalDoesNotEscapeHTML(t *testing.T) {
	got, err := Marshal(map[string]any{"h": "<a>&'</a>"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Contains(got, []byte(`<a>&'</a>`)) {
		t.Fatalf("output = %s, want unescaped HTML", got)
	}
	if bytes.Contains(got, []byte(`\u003`)) {
		t.Fatalf("output = %s, want no HTML escapes", got)
	}
}

// Invalid UTF-8 in Go string fields becomes U+FFFD exactly as the v1 API did
// (byte-level equality), instead of failing the encode.
func TestMarshalReplacesInvalidUTF8LikeV1(t *testing.T) {
	bad := "\xff\xfe ok \xc3\x28 end"
	v1, err := json.Marshal(map[string]any{"s": bad})
	if err != nil {
		t.Fatalf("v1 marshal: %v", err)
	}
	got, err := Marshal(map[string]any{"s": bad})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != string(v1) {
		t.Fatalf("output mismatch:\n v1=%x\n v2=%x", v1, got)
	}
}

func TestEncodersPreserveRawMessage(t *testing.T) {
	raw := json.RawMessage(`{"letter":"\u0041","slash":"a\/b","html":"\u003cfoo\u003e","number":1e1000,"x":1,"x":2,"note":"v` + "\xff\xfe\u2028\u2029" + `"}`)
	value := sample{Name: "a\u2028b\u2029c", Raw: raw}
	compact, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		encode func() ([]byte, error)
		want   []byte
	}{
		{"Marshal", func() ([]byte, error) { return Marshal(value) }, compact},
		{"MarshalIndent", func() ([]byte, error) { return MarshalIndent(value, "", "  ") }, indented},
		{"Encode", func() ([]byte, error) {
			var buf bytes.Buffer
			err := Encode(&buf, value)
			return buf.Bytes(), err
		}, append(bytes.Clone(compact), '\n')},
		{"EncodeIndent", func() ([]byte, error) {
			var buf bytes.Buffer
			err := EncodeIndent(&buf, value, "", "  ")
			return buf.Bytes(), err
		}, append(bytes.Clone(indented), '\n')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.encode()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("output mismatch:\n want=%q\n got=%q", tc.want, got)
			}
		})
	}
}

// nil slices and maps marshal as null without omitempty, matching v1.
func TestMarshalNilContainersAsNull(t *testing.T) {
	type doc struct {
		S []int          `json:"s"`
		M map[string]int `json:"m"`
	}
	v1, err := json.Marshal(doc{})
	if err != nil {
		t.Fatalf("v1 marshal: %v", err)
	}
	got, err := Marshal(doc{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != string(v1) || !bytes.Contains(got, []byte(`"s":null,"m":null`)) {
		t.Fatalf("output = %s, want v1 bytes %s", got, v1)
	}
}

// MarshalIndent matches v1's shape, including the absence of a trailing
// newline.
func TestMarshalIndentMatchesV1(t *testing.T) {
	value := sample{Name: "main", Attrs: map[string]any{"b": 2, "a": 1}}
	v1, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("v1 marshal indent: %v", err)
	}
	got, err := MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if string(got) != string(v1) {
		t.Fatalf("output mismatch:\n v1=%q\n v2=%q", v1, got)
	}
	if len(got) > 0 && got[len(got)-1] == '\n' {
		t.Fatal("MarshalIndent must not end with a newline")
	}
}

// Encode reproduces json.Encoder's newline-terminated single value.
func TestEncodeAppendsNewline(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, sample{Name: "main"}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("}\n")) {
		t.Fatalf("output = %q, want newline-terminated value", buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte(`\u003`)) {
		t.Fatalf("output = %s, want no HTML escapes", buf.String())
	}
	// The output must be exactly one JSON document, decodable by a strict
	// single-value decoder.
	dec := jsontext.NewDecoder(bytes.NewReader(buf.Bytes()))
	if _, err := dec.ReadValue(); err != nil {
		t.Fatalf("ReadValue: %v", err)
	}
	if _, err := dec.ReadValue(); err == nil {
		t.Fatal("second ReadValue: want EOF after single document")
	}
}

// EscapeForPrompt escapes <, > and & in canonically encoded text without
// disturbing the encoder's own escapes or the surrounding JSON structure.
func TestEscapeForPrompt(t *testing.T) {
	encoded, err := Marshal(map[string]any{"s": "<a>&'</a>", "n": 1})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(EscapeForPrompt(encoded))
	// Only <, > and & are escaped; the apostrophe stays literal.
	want := `{"n":1,"s":"\u003ca\u003e\u0026'\u003c/a\u003e"}`
	if got != want {
		t.Fatalf("output = %s, want %s", got, want)
	}
	// The result must still be one valid JSON document decoding to the
	// original value.
	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("escaped output is not valid JSON: %v", err)
	}
	if back["s"] != "<a>&'</a>" {
		t.Fatalf("decoded value = %v, want the original string", back["s"])
	}
}
