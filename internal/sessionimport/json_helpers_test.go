package sessionimport

import (
	"encoding/json"
	"testing"
)

func TestImportJSONInputContract(t *testing.T) {
	for _, decode := range []struct {
		name string
		fn   func([]byte, any) error
	}{
		{"bytes", importJSONUnmarshal},
		{"string", func(data []byte, v any) error { return importJSONUnmarshalString(string(data), v) }},
	} {
		t.Run(decode.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, raw, want string
				wantErr         bool
			}{
				{"unknown fields", `{"text":"value","extra":1e1000}`, "value", false},
				{"case insensitive fields", `{"TEXT":"value"}`, "value", false},
				{"duplicate fields", `{"text":"first","text":"last"}`, "last", false},
				{"escaped tab", `{"text":"a\tb"}`, "a\tb", false},
				{"invalid UTF-8", "{\"text\":\"a\xffb\"}", "a\ufffdb", false},
				{"bare tab", "{\"text\":\"a\tb\"}", "", true},
				{"trailing value", `{"text":"value"} {}`, "", true},
				{"truncated", `{"text":"value"`, "", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var v struct {
						Text string `json:"text"`
					}
					err := decode.fn([]byte(tc.raw), &v)
					if (err != nil) != tc.wantErr {
						t.Fatalf("error=%v, wantErr=%v", err, tc.wantErr)
					}
					if !tc.wantErr && v.Text != tc.want {
						t.Fatalf("text=%q, want %q", v.Text, tc.want)
					}
				})
			}
			var obj map[string]json.RawMessage
			if err := decode.fn([]byte(`{"extra":1e1000}`), &obj); err != nil {
				t.Fatal(err)
			}
			if string(obj["extra"]) != "1e1000" {
				t.Fatalf("raw number changed: %s", obj["extra"])
			}
		})
	}
}
