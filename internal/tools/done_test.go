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
	reason, ok := props["report"].(map[string]any)
	if !ok {
		t.Fatalf("Done tool report property type = %T", props["report"])
	}
	if got := reason["description"]; got != "Detailed final report in Markdown: completion status, summary of work, verification status, and remaining limitations." {
		t.Fatalf("report description = %v", got)
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
		{name: "blank report", raw: `{"report":"   "}`, want: "Done requested"},
		{name: "report only", raw: `{"report":"Implementation complete."}`, want: "Implementation complete."},
		{name: "trimmed report", raw: `{"report":"  verified  "}`, want: "verified"},
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
