package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

var errDirectorySpeculationDeferred = errors.New("directory instruction coverage requires formal dispatch")

// directoryInstructionsCovered is deliberately conservative: only paths whose
// canonical identity was already discovered can enter speculative execution.
// It performs no filesystem operations and reads immutable snapshots only.
func (p toolExecutionPipeline) directoryInstructionsCovered(tc message.ToolCall) bool {
	p = p.directoryAttemptSnapshot()
	if p.directoryRuntime == nil {
		return true
	}
	if p.directoryRequest == nil || p.directoryRequest.Binding != p.directoryRuntime.binding {
		return false
	}
	if p.directoryGate.Reason() != "" {
		return false
	}
	p = p.withMachineStateBaseDir(tc)
	if p.currentRuleset != nil {
		_, identity := p.directoryInstructionReadPermission()
		if identity != p.directoryRuntime.permissionIdentity {
			return false
		}
	}
	targets, err := tools.InstructionTargetsWithoutIO(tc.Name, tc.Args, p.effectiveToolBaseDir())
	if err != nil {
		return false
	}
	frozen := p.directoryRuntime.frozen.Load()
	if frozen == nil {
		return len(targets) == 0
	}
	for _, target := range targets {
		dir, known := frozen.Targets[target]
		if !known && tc.Name == tools.NameGrep {
			target.Mode = tools.InstructionTargetDirectory
			dir, known = frozen.Targets[target]
		}
		if !known {
			// Entry operations do not follow the leaf, so a frozen canonical parent
			// chain also covers a new sibling filename without filesystem work.
			switch target.Mode {
			case tools.InstructionTargetEntry:
				dir = filepath.Dir(target.Path)
			case tools.InstructionTargetDirectory:
				dir = target.Path
			default:
				return false
			}
		}
		if dir == "" {
			continue
		}
		scopes, inside := instructionScopeChain(p.directoryRuntime.binding.CheckoutRoot, dir)
		if !inside {
			return false
		}
		for _, scope := range scopes {
			observation, known := frozen.Observations[scope]
			if !known || observation.Status == "blocked" {
				return false
			}
			if observation.Status == "present" {
				ref := directoryInstructionFactFromObservation(p.directoryRuntime.binding, scope, observation).Ref()
				if _, visible := p.directoryRequest.Visible[ref]; !visible {
					return false
				}
			}
		}
	}
	return true
}

func (p toolExecutionPipeline) directoryInstructionSeparateBatch(tc message.ToolCall) bool {
	if p.syncHookGate != nil && p.syncHookGate() {
		return true
	}
	if p.directoryRuntime == nil {
		return false
	}
	if directoryInstructionPreflightTool(tc.Name) && !p.directoryInstructionsCovered(tc) {
		return true
	}
	ruleset := permission.Ruleset(nil)
	if p.currentRuleset != nil {
		ruleset = p.currentRuleset()
	}
	// A possible ask is enough to isolate the call. Exact path evaluation can
	// canonicalize external paths and belongs to worker permission preparation.
	for _, rule := range ruleset {
		if rule.Action == permission.ActionAsk && (p.yoloDowngradeAsk == nil || !p.yoloDowngradeAsk(tc.Name)) {
			return true
		}
	}
	return false
}

func (r streamToolDeltaReducer) directorySpeculativePrefixCovered(callID, name, args string) bool {
	if r.directoryCovered == nil {
		return true
	}
	for _, prior := range r.turn.streamingToolCallsBefore(callID) {
		if !r.directoryCovered(message.ToolCall{ID: prior.CallID, Name: tools.NormalizeName(prior.Name), Args: json.RawMessage(prior.ArgsJSON)}) {
			return false
		}
	}
	return r.directoryCovered(message.ToolCall{ID: callID, Name: name, Args: json.RawMessage(args)})
}
