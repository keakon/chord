package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestStreamToolPermissionRevocation(t *testing.T) {
	for _, worker := range []bool{false, true} {
		name := "main"
		if worker {
			name = "subagent"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			a := newTestMainAgent(t, root)
			a.tools.Register(tools.ReadTool{})
			a.initOverlay()
			a.overlay.SetActiveRole(a.currentAgentName())
			a.overlay.SetBase(permission.Ruleset{{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionDeny}})
			a.applyOverlayRulesetChange()
			if err := a.AddOverlayRule(permission.Rule{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow}, permission.ScopeSession); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"first.txt", "second.txt"} {
				if err := os.WriteFile(filepath.Join(root, file), []byte("sample "+file+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			turn := newStreamToolReducerTestTurn()
			turn.Ctx = ctx
			var reducer *llmStreamReducer
			execute, bind := a.executeToolCallSpeculative, a.bindSpeculativeEntry
			ruleset, scope := a.effectiveRuleset, a.effectivePathScope()
			if worker {
				sub := newControllableTestSubAgent(t, a, "sample-task")
				sub.setRuleset(a.buildSubAgentRuleset(nil))
				execute, bind = sub.executeToolCallSpeculative, sub.bindSpeculativeEntry
				ruleset, scope = sub.currentRuleset, sub.effectivePathScope()
				reducer = sub.newSubLLMStreamReducer(turn, func(string) {}, false, nil, 0)
			} else {
				reducer = a.newMainLLMStreamReducer(nil, "sample/test-model", "", turn, false, nil, 0)
			}
			executor := NewStreamingToolExecutor(turn.ID, ctx, nil, execute)
			executor.SetEntryBinder(bind)
			executor.SetWorkDir(root)
			turn.streamingToolExec = executor
			call := func(id, file string) {
				t.Helper()
				args, err := json.Marshal(map[string]string{"path": filepath.Join(root, file)})
				if err != nil {
					t.Fatal(err)
				}
				reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: id, Name: tools.NameRead, Input: string(args)}})
				reducer.Handle(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: id}})
			}
			entry := func(id string) *streamingToolEntry {
				t.Helper()
				executor.mu.Lock()
				got := executor.entries[id]
				executor.mu.Unlock()
				if got != nil {
					select {
					case <-got.done:
					case <-ctx.Done():
						t.Fatalf("call %s did not finish: %v", id, ctx.Err())
					}
				}
				return got
			}
			call("call-1", "first.txt")
			first := entry("call-1")
			if first == nil || first.err != nil || !first.result.ExecutionStarted || !strings.Contains(first.result.Result, "sample first.txt") {
				t.Fatalf("initial allowed read did not execute successfully: %+v", first)
			}
			if err := a.RemoveOverlayAddedRule(0); err != nil {
				t.Fatal(err)
			}
			if got := evaluateToolPermissionInDir(ruleset(), tools.NameRead, json.RawMessage(`{"path":"second.txt"}`), scope); got.Action != permission.ActionDeny {
				t.Fatalf("current permission = %v, want deny", got.Action)
			}
			call("call-2", "second.txt")
			if second := entry("call-2"); second != nil {
				t.Fatalf("revoked read was admitted: error=%v result=%q", second.err, second.result.Result)
			}
		})
	}
}
