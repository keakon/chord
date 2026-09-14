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
	// The tool is mounted only while a loop runs, so the argument description
	// states what to write rather than re-litigating whether to call the tool.
	if !strings.Contains(desc, "completion status, changes, verification, and remaining issues") {
		t.Fatalf("report description missing report contents: %q", desc)
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("required type = %T", params["required"])
	}
	if len(required) != 1 || required[0] != "report" {
		t.Fatalf("required = %v, want [report]", required)
	}
}

// The runtime mounts done only while a loop is active, so its description is
// written for that single situation: it states the exit bar instead of
// arguing against being called, which is what the old always-mounted wording
// had to spend half its length on.
func TestDoneToolDescriptionTargetsActiveLoop(t *testing.T) {
	desc := NewDoneTool().Description()
	for _, want := range []string{
		"Requests exit from the active loop workflow",
		"only when the current objective is fully complete",
		"no other tool call is necessary or appropriate",
		"no blocker or unresolved user decision remains",
		"Required verification must be completed, or explicitly reported as not run",
		"with the reason it could not be run",
		"Never call it for partial progress",
		"continue working instead of calling `done`",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Done description missing %q: %q", want, desc)
		}
	}
	// The tool is absent outside a loop, so the description must not spend
	// tokens telling the model when not to call it.
	for _, unwanted := range []string{"otherwise DO NOT call it", "return the final answer directly as assistant text", "user approval", "no unresolved user decision, error, or verification remains"} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("Done description still carries not-mounted guidance %q: %q", unwanted, desc)
		}
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
