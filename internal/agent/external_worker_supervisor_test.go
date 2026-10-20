//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// The managed process must not be Chord's own child: Chord can be killed, and
// only a supervisor that outlives it can still tear the process group down.
func TestExternalWorkerRunsUnderItsOwnSupervisor(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "parent")
	script := writeFakeClaude(t, `
printf '%s\n' "$PPID" > "`+marker+`"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"supervised"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "prove the worker is supervised",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if record := driveExternalTask(t, a, handle.TaskID); record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (summary=%q)", record.State, record.LastSummary)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read parent marker: %v", err)
	}
	parent, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parent marker = %q: %v", data, err)
	}
	if parent == os.Getpid() {
		t.Fatal("the external process is Chord's own child; it has no supervisor to clean it up")
	}
	if parent <= 1 {
		t.Fatalf("parent = %d, want the supervisor's pid", parent)
	}
}
