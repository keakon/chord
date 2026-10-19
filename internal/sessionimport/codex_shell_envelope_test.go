package sessionimport

import "testing"

func TestParseCodexShellResult(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantOK      bool
		wantExit    int
		wantSettled bool
		wantBody    string
	}{
		{
			name:        "settled success",
			output:      "Chunk ID: 58e21d\nWall time: 0.0520 seconds\nProcess exited with code 0\nOriginal token count: 3\nOutput:\ntotal 8\n",
			wantOK:      true,
			wantSettled: true,
			wantBody:    "total 8\n",
		},
		{
			name:        "settled failure",
			output:      "Chunk ID: 8902b5\nWall time: 0.0000 seconds\nProcess exited with code 1\nOriginal token count: 271\nOutput:\n# chord/internal/sessionimport\nFAIL\n",
			wantOK:      true,
			wantExit:    1,
			wantSettled: true,
			wantBody:    "# chord/internal/sessionimport\nFAIL\n",
		},
		{
			name:        "signalled process",
			output:      "Wall time: 3.0000 seconds\nProcess exited with code -1\nOutput:\nkilled\n",
			wantOK:      true,
			wantExit:    -1,
			wantSettled: true,
			wantBody:    "killed\n",
		},
		{
			name:   "still running has no exit status",
			output: "Chunk ID: 1919aa\nWall time: 10.0000 seconds\nProcess running with session ID 481\nOriginal token count: 7\nOutput:\npartial",
			wantOK: true,
			// A yielded result is not an outcome: ExitCode must stay unused so
			// the caller neither hides it nor reads it as success.
			wantSettled: false,
			wantBody:    "partial",
		},
		{
			// The older envelope wrote no status line at all; only a finished
			// process produced it, so the body is final and the status stays 0.
			name:        "older header without a status line",
			output:      "Wall time: 0.1234 seconds\nOutput:\n/tmp\n",
			wantOK:      true,
			wantSettled: true,
			wantBody:    "/tmp\n",
		},
		{
			name:        "echoed command spans lines",
			output:      "Command: /bin/zsh -lc \"python3 - <<'PY'\nimport csv\nprint(1)\nPY\"\nChunk ID: 11aa22\nWall time: 0.0000 seconds\nProcess exited with code 2\nOriginal token count: 5\nOutput:\nboom\n",
			wantOK:      true,
			wantExit:    2,
			wantSettled: true,
			wantBody:    "boom\n",
		},
		{
			name:        "empty body",
			output:      "Chunk ID: 052723\nWall time: 0.0000 seconds\nProcess exited with code 0\nOriginal token count: 0\nOutput:",
			wantOK:      true,
			wantSettled: true,
			wantBody:    "",
		},
		{
			name:        "body keeps its own output marker",
			output:      "Chunk ID: aa11bb\nWall time: 0.0000 seconds\nProcess exited with code 0\nOutput:\nnested:\nOutput:\ninner\n",
			wantOK:      true,
			wantSettled: true,
			wantBody:    "nested:\nOutput:\ninner\n",
		},
		{
			// The `exec` script envelope names its status on the first line and
			// writes "Wall time 1.2 seconds" without the colon.
			name:   "exec envelope is not this one",
			output: "Script completed\nWall time 0.1 seconds\nOutput:\n## main\n",
			wantOK: false,
		},
		{
			name:   "plain output",
			output: "some result",
			wantOK: false,
		},
		{
			name:   "diff that mentions both markers",
			output: "-Wall time: 1.0 seconds\n+Wall time: 2.0 seconds\n-Output:\n+Output:\n",
			wantOK: false,
		},
		{
			name:   "marker without a header",
			output: "Output:\nonly a body\n",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCodexShellResult(tt.output)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v (result=%+v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Settled != tt.wantSettled || got.ExitCode != tt.wantExit || got.Output != tt.wantBody {
				t.Fatalf("result=%+v, want settled=%v exit=%d body=%q", got, tt.wantSettled, tt.wantExit, tt.wantBody)
			}
		})
	}
}

func TestCodexHasShellEnvelope(t *testing.T) {
	for _, name := range []string{"exec_command", "write_stdin", "shell", " exec_command "} {
		if !codexHasShellEnvelope(name) {
			t.Fatalf("%q should carry the shell envelope", name)
		}
	}
	// A tool that returns file content can hold the same header lines, so only
	// the tools that write the envelope may be decoded as one.
	for _, name := range []string{"read_file", "grep", "exec", "apply_patch", ""} {
		if codexHasShellEnvelope(name) {
			t.Fatalf("%q must not be decoded as a shell envelope", name)
		}
	}
}
