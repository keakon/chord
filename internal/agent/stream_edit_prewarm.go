package agent

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type editFilePrewarmer interface {
	PrewarmFile(context.Context, string, func() bool)
}

type streamEditPrewarm struct {
	ctx    context.Context
	cancel context.CancelFunc
	calls  map[string]*streamEditPrewarmCall
}

type streamEditPrewarmCall struct {
	name     string
	scanner  *tools.EditIntentScanner
	freeform bool
	seen     map[tools.ApplyPatchDisplayTarget]bool
	prior    []PendingToolCall
	tool     editFilePrewarmer
}

func (r *streamToolDeltaReducer) prewarmEditStart(call *message.ToolCallDelta) {
	if r.turn == nil || r.turn.Ctx == nil || r.registry == nil || call.ID == "" || call.DeferExecution ||
		(r.syncHookGate != nil && r.syncHookGate()) {
		return
	}
	name := tools.NormalizeName(call.Name)
	scanner := tools.NewEditIntentScanner(name)
	if scanner == nil {
		return
	}
	tool, ok := r.registry.Get(name)
	if !ok {
		return
	}
	prewarmer, ok := tool.(editFilePrewarmer)
	if !ok {
		return
	}
	if r.prewarm == nil {
		ctx, cancel := context.WithCancel(r.turn.Ctx)
		r.prewarm = &streamEditPrewarm{ctx: ctx, cancel: cancel, calls: make(map[string]*streamEditPrewarmCall)}
	}
	if len(r.prewarm.calls) >= 32 {
		return
	}
	r.prewarm.calls[call.ID] = &streamEditPrewarmCall{
		name: name, scanner: scanner, tool: prewarmer,
		seen:  make(map[tools.ApplyPatchDisplayTarget]bool),
		prior: r.turn.streamingToolCallsBefore(call.ID),
	}
	r.prewarmEditDelta(call)
}

func (r *streamToolDeltaReducer) prewarmEditDelta(call *message.ToolCallDelta) {
	if r.prewarm == nil || r.prewarm.ctx.Err() != nil || call.DeferExecution {
		return
	}
	state := r.prewarm.calls[call.ID]
	if state == nil {
		return
	}
	fragment := call.Input
	if call.InputText != "" {
		if !state.freeform {
			state.scanner = tools.NewEditIntentScanner(state.name)
			state.freeform = true
		}
		fragment = call.InputText
	} else if state.freeform {
		return
	}
	state.scanner.Feed(fragment, func(target tools.ApplyPatchDisplayTarget) {
		if len(state.seen) >= 64 || state.seen[target] {
			return
		}
		state.seen[target] = true
		path := target.SourcePath
		if target.TargetPath != "" {
			path = target.TargetPath
		}
		if filepath.Ext(path) != ".go" || (r.syncHookGate != nil && r.syncHookGate()) {
			return
		}
		ruleset := permission.Ruleset(nil)
		if r.ruleset != nil {
			ruleset = r.ruleset()
		}
		// Rulesets are replaced, never mutated. Capture all other inputs too:
		// authorization may resolve symlinks and must run off the stream path.
		scope, registry, prior, name := r.pathScope, r.registry, state.prior, state.name
		state.tool.PrewarmFile(r.prewarm.ctx, path, func() bool {
			// Interleaved calls can leave incomplete snapshots whose missing
			// permission argument would otherwise match a wildcard allow.
			for _, call := range prior {
				tool, ok := registry.Get(tools.NormalizeName(call.Name))
				if !ok || tools.ValidateToolArgs(tool, llm.UnwrapToolArgs(json.RawMessage(call.ArgsJSON))) != nil {
					return false
				}
			}
			if _, blocked := firstBlockingPriorSpeculativeCall(registry, ruleset, prior, scope); blocked {
				return false
			}
			if name == tools.NameApplyPatch {
				return evaluateApplyPatchTargetsPermissionInDir(ruleset, []tools.ApplyPatchDisplayTarget{target}, scope).Action == permission.ActionAllow
			}
			return ruleset.EvaluatePath(name, path, scope) == permission.ActionAllow
		})
	})
}
