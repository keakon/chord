package tools

import (
	"context"
	"strings"
	"testing"
)

func TestDoneToolParameters(t *testing.T) {
	params := NewDoneTool().Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", params["properties"])
	}
	report, ok := props["report"].(map[string]any)
	if !ok {
		t.Fatalf("report schema type = %T", props["report"])
	}
	if report["type"] != "string" {
		t.Fatalf("report type = %v, want string", report["type"])
	}
	desc, _ := report["description"].(string)
	if !strings.Contains(desc, "user's current language") {
		t.Fatalf("report description missing user language guidance: %q", desc)
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("required type = %T", params["required"])
	}
	if len(required) != 1 || required[0] != "report" {
		t.Fatalf("required = %v, want [report]", required)
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
		{name: "null args", raw: `null`, wantErr: true},
		{name: "empty object", raw: `{}`, wantErr: true},
		{name: "blank report", raw: `{"report":"   "}`, wantErr: true},
		{name: "no args", raw: ``, wantErr: true},
		{name: "with report", raw: `{"report":"## Completion status\nDone\n\n## Verification\n- tested"}`, want: "Done requested: report received (51 chars)"},
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

func TestParseDoneArgs(t *testing.T) {
	args, err := ParseDoneArgs([]byte(`{"report":"  final report  "}`))
	if err != nil {
		t.Fatalf("ParseDoneArgs: %v", err)
	}
	if args.Report != "final report" {
		t.Fatalf("Report = %q, want final report", args.Report)
	}
}
