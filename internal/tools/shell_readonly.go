package tools

import (
	"encoding/json"

	"github.com/keakon/chord/internal/shellanalysis"
)

// shellReadOnlyArgsVerdict adapts the classifier to tool JSON args plus the
// ShellTool instance's shell type. JSON or command extraction failures are
// fail-closed.
func shellReadOnlyArgsVerdict(args json.RawMessage, shellType string) shellanalysis.ReadOnlyVerdict {
	var parsed struct {
		Command         string `json:"command"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return shellanalysis.ReadOnlyVerdict{Reason: "bad_args"}
	}
	return shellanalysis.ClassifyReadOnly(parsed.Command, shellType, parsed.RunInBackground)
}
