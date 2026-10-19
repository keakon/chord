package sessionimport

import "testing"

func TestParseCodexResultPayload(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantOK   bool
		wantExit int
		wantBody string
	}{
		{
			name:     "success",
			text:     `{"output":"Success. Updated the following files:\nM a.txt\n","metadata":{"exit_code":0,"duration_seconds":0.0}}`,
			wantOK:   true,
			wantBody: "Success. Updated the following files:\nM a.txt\n",
		},
		{
			name:     "failure",
			text:     `{"output":"FAIL\n","metadata":{"exit_code":1,"duration_seconds":0.1}}`,
			wantOK:   true,
			wantExit: 1,
			wantBody: "FAIL\n",
		},
		{
			name:     "extra fields",
			text:     `{"output":"ok\n","metadata":{"exit_code":0,"duration_seconds":0.0},"traceback":null}`,
			wantOK:   true,
			wantBody: "ok\n",
		},
		{
			name:     "empty output",
			text:     `{"output":"","metadata":{"exit_code":0}}`,
			wantOK:   true,
			wantBody: "",
		},
		{
			name:   "output is not a string",
			text:   `{"output":{"text":"ok"},"metadata":{"exit_code":0}}`,
			wantOK: false,
		},
		{
			name:   "no metadata",
			text:   `{"output":"ok\n"}`,
			wantOK: false,
		},
		{
			name:   "no exit code",
			text:   `{"output":"ok\n","metadata":{"duration_seconds":0.1}}`,
			wantOK: false,
		},
		{
			name:   "not an object",
			text:   `[{"output":"ok\n","metadata":{"exit_code":0}}]`,
			wantOK: false,
		},
		{
			name:   "trailing text",
			text:   "{\"output\":\"ok\\n\",\"metadata\":{\"exit_code\":0}}\nsecond line",
			wantOK: false,
		},
		{
			name:   "leading text",
			text:   "Output:\n{\"output\":\"ok\\n\",\"metadata\":{\"exit_code\":0}}",
			wantOK: false,
		},
		{
			name:   "payload-shaped without the payload",
			text:   `{"result":"ok"}`,
			wantOK: false,
		},
		{
			name:   "plain text",
			text:   "FAIL\n",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCodexResultPayload(tt.text)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v (payload=%+v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got.ExitCode != tt.wantExit || got.Output != tt.wantBody {
				t.Fatalf("payload=%+v, want exit=%d body=%q", got, tt.wantExit, tt.wantBody)
			}
		})
	}
}

func TestCodexRecordsResultPayload(t *testing.T) {
	// These tools record FunctionCallOutputPayload instead of their own output.
	for _, name := range []string{"exec_command", "write_stdin", "shell", "apply_patch", " apply_patch "} {
		if !codexRecordsResultPayload(name) {
			t.Fatalf("%q should be decoded as a payload", name)
		}
	}
	// A file's own content is never decoded as one.
	for _, name := range []string{"read_file", "grep", "exec", "write_file", ""} {
		if codexRecordsResultPayload(name) {
			t.Fatalf("%q must not be decoded as a payload", name)
		}
	}
}
