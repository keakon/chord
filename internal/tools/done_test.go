package tools

import (
	"context"
	"testing"
)

func TestDoneToolParameters(t *testing.T) {
	params := NewDoneTool().Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", params["properties"])
	}
	if _, ok := props["reason"]; !ok {
		t.Fatalf("Done tool parameters missing reason property: %#v", props)
	}
}

func TestDoneToolExecute(t *testing.T) {
	tool := NewDoneTool()
	ctx := context.Background()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "null args", raw: `null`, want: "Done requested"},
		{name: "empty object", raw: `{}`, want: "Done requested"},
		{name: "blank reason", raw: `{"reason":"   "}`, want: "Done requested"},
		{name: "reason only", raw: `{"reason":"Implementation complete."}`, want: "Implementation complete."},
		{name: "trimmed reason", raw: `{"reason":"  verified  "}`, want: "verified"},
		{name: "invalid json", raw: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tool.Execute(ctx, []byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Execute() = %q, want %q", got, tt.want)
			}
		})
	}
}
