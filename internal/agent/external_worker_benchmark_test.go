package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/toolname"
)

func newExternalBenchmarkWorker(b *testing.B) *externalWorker {
	b.Helper()
	dir := b.TempDir()
	manager := recovery.NewRecoveryManager(dir)
	b.Cleanup(func() {
		if err := manager.Close(); err != nil {
			b.Error(err)
		}
	})
	a := &MainAgent{
		sessionDir: dir, outputCh: make(chan AgentEvent, 16),
		recoveryOwner: &recoveryOwnership{manager: manager, epoch: 0, dir: dir},
	}
	return &externalWorker{
		parent: a, ctx: context.Background(), instanceID: "runtime-1", taskID: "task-1",
		workDir: dir, ctxMgr: ctxmgr.NewManager(0, 0),
		toolNames: make(map[string]string), toolSeen: make(map[string]bool),
		toolPreWrites: make(map[string]externalToolPreWrite), toolBindings: make(map[string]*externalToolBinding),
		ruleset: permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}},
	}
}

func BenchmarkExternalToolAuthorization(b *testing.B) {
	for _, size := range []int{512, 64 << 10, 256 << 10, 1 << 20} {
		name := fmt.Sprintf("bytes=%d", size)
		if size == 1<<20 {
			name += "/oversized-rejection"
		}
		b.Run(name, func(b *testing.B) {
			w := newExternalBenchmarkWorker(b)
			input, err := json.Marshal(map[string]string{
				"file_path": filepath.Join(w.workDir, "sample.txt"),
				"content":   strings.Repeat("x", size),
			})
			if err != nil {
				b.Fatal(err)
			}
			event := externalclaude.Event{RequestID: "permission-1", Request: &externalclaude.ControlRequest{
				ToolName: "Write", ToolUseID: "tool-1", Input: input,
			}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				delete(w.toolBindings, "tool-1")
				delete(w.toolSeen, "tool-1")
				w.ctxMgr = ctxmgr.NewManager(0, 0)
				w.persistence = agentPersistenceHealth{}
				w.setResultError(nil)
				b.StartTimer()
				w.externalToolStateMu.Lock()
				w.handleExternalToolUse(externalclaude.ContentBlock{ID: "tool-1", Name: "Write", Input: input})
				w.externalToolStateMu.Unlock()
				fingerprint, err := externalToolFingerprint(event.Request.ToolName, event.Request.Input)
				if err != nil {
					b.Fatal(err)
				}
				frame := w.evaluateExternalPermission(w.ctx, event, fingerprint)
				b.StopTimer()
				var decision externalclaude.PermissionDecision
				if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
					b.Fatal(err)
				}
				if size == 1<<20 {
					if decision.Behavior != externalclaude.PermissionBehaviorDeny || w.toolBindings["tool-1"].started ||
						w.getResultError() == nil || !strings.Contains(w.getResultError().Error(), "recovery size limit") {
						b.Fatalf("oversized audit was not rejected before authorization: decision=%+v err=%v", decision, w.getResultError())
					}
				} else if decision.Behavior != externalclaude.PermissionBehaviorAllow || w.getResultError() != nil {
					b.Fatalf("authorization failed: decision=%+v err=%v", decision, w.getResultError())
				}
				for len(w.parent.outputCh) > 0 {
					<-w.parent.outputCh
				}
				b.StartTimer()
			}
		})
	}
}

func BenchmarkExternalToolResultWithHistory(b *testing.B) {
	for _, history := range []int{1, 100, 10000} {
		b.Run(fmt.Sprintf("settled=%d", history), func(b *testing.B) {
			w := newExternalBenchmarkWorker(b)
			for i := range history {
				w.toolBindings[fmt.Sprintf("settled-%d", i)] = &externalToolBinding{started: true, settled: true}
			}
			w.toolBindings["active-1"] = &externalToolBinding{started: true}
			result := externalclaude.ContentBlock{ToolUseID: "active-1", Content: json.RawMessage(`"Sample result"`)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				w.toolBindings["active-1"].settled = false
				w.rememberToolName("active-1", toolname.Read)
				w.lastActivity = ActivityExecuting
				b.StartTimer()
				w.externalToolStateMu.Lock()
				handled := w.handleExternalToolResult(result)
				w.externalToolStateMu.Unlock()
				b.StopTimer()
				if !handled || w.getResultError() != nil || !w.toolBindings["active-1"].settled {
					b.Fatalf("result not durably settled: handled=%t err=%v", handled, w.getResultError())
				}
				for len(w.parent.outputCh) > 0 {
					<-w.parent.outputCh
				}
				b.StartTimer()
			}
		})
	}
}
