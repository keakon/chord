package agent

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type directoryInstructionAttempt struct {
	request directoryInstructionRequest
	gate    *directoryInstructionGate
}

func (p toolExecutionPipeline) directoryAttemptSnapshot() toolExecutionPipeline {
	if p.directoryRuntime != nil && p.directoryRequest == nil {
		if attempt := p.directoryRuntime.attempt.Load(); attempt != nil {
			p.directoryRequest = &attempt.request
			p.directoryGate = attempt.gate
		}
	}
	return p
}
func directoryInstructionPreflightTool(name string) bool {
	switch name {
	case tools.NameWrite, tools.NameEdit, tools.NameApplyPatch, tools.NameDelete, tools.NameShell:
		return true
	}
	return false
}
func directoryInstructionReadTool(name string) bool {
	switch name {
	case tools.NameRead, tools.NameGlob, tools.NameGrep:
		return true
	}
	return false
}
func (p toolExecutionPipeline) stopForDirectoryInstructions(result *ToolExecutionResult, reason directoryInstructionStopReason) error {
	p.directoryGate.Close(reason)
	result.ControlReason = string(reason)
	switch reason {
	case directoryInstructionStopRequired:
		return fmt.Errorf("not executed: directory rules must be loaded before reconsidering this operation")
	case directoryInstructionStopBlocked:
		return fmt.Errorf("directory rules could not be loaded; inspect the affected path and permissions before retrying")
	default:
		return fmt.Errorf("not executed: a preceding operation stopped this response for directory rules")
	}
}
func (p toolExecutionPipeline) directoryDiscoveryReason(d directoryInstructionDiscovery) directoryInstructionStopReason {
	if !d.Complete {
		return directoryInstructionStopBlocked
	}
	blockedScopes := make(map[string]struct{})
	if p.directoryRuntime != nil {
		if view := p.directoryRuntime.view.Load(); view != nil {
			for _, fact := range view.Blocked {
				blockedScopes[fact.Scope] = struct{}{}
			}
		}
	}
	for scope, o := range d.Observations {
		if _, blocked := blockedScopes[scope]; blocked {
			return directoryInstructionStopBlocked
		}
		if o.Status == "blocked" {
			return directoryInstructionStopBlocked
		}
		if o.Status != "present" {
			continue
		}
		ref := directoryInstructionFactFromObservation(d.Binding, scope, o).Ref()
		if p.directoryRequest == nil {
			return directoryInstructionStopRequired
		}
		if _, visible := p.directoryRequest.Visible[ref]; !visible {
			return directoryInstructionStopRequired
		}
	}
	return ""
}
func (p toolExecutionPipeline) discoverToolDirectoryInstructions(ctx context.Context, tc message.ToolCall, result *ToolExecutionResult) error {
	targets, err := tools.InstructionTargets(tc.Name, tc.Args, p.effectiveToolBaseDir())
	if err != nil {
		p.directoryGate.Close(directoryInstructionStopBlocked)
		result.ControlReason = string(directoryInstructionStopBlocked)
		return fmt.Errorf("directory target could not be resolved: %w", err)
	}
	return p.discoverDirectoryInstructionTargets(ctx, tc, targets, result)
}

func (p toolExecutionPipeline) discoverDirectoryInstructionTargets(ctx context.Context, tc message.ToolCall, targets []tools.InstructionTarget, result *ToolExecutionResult) error {
	if len(targets) == 0 {
		return nil
	}
	if p.directoryRequest != nil {
		ctx = withDirectoryInstructionDiscoveryRequest(ctx, p.directoryRequest.RequestSeq)
	}
	if p.currentRuleset != nil {
		check, identity := p.directoryInstructionReadPermission()
		ctx = context.WithValue(ctx, directoryInstructionPermissionContextKey{}, directoryInstructionPermissionOverride{check: check, identity: identity})
	}
	d, err := p.directoryRuntime.Discover(ctx, p.directoryRuntime.binding, targets)
	d.CallID = tc.ID
	if p.directoryRequest != nil {
		d.RequestSeq = p.directoryRequest.RequestSeq
		d.AttemptSeq = p.directoryRequest.AttemptSeq
	}
	result.DirectoryDiscovery = &d
	if err != nil {
		return p.stopForDirectoryInstructions(result, directoryInstructionStopBlocked)
	}
	if reason := p.directoryDiscoveryReason(d); reason != "" {
		return p.stopForDirectoryInstructions(result, reason)
	}
	return nil
}
func (p toolExecutionPipeline) admitDirectoryInstructionTool(ctx context.Context, tc message.ToolCall, result *ToolExecutionResult) error {
	if p.directoryRuntime == nil {
		return nil
	}
	if p.directoryGate.Reason() != "" {
		return p.stopForDirectoryInstructions(result, directoryInstructionStopReplan)
	}
	if directoryInstructionPreflightTool(tc.Name) {
		if err := p.discoverToolDirectoryInstructions(ctx, tc, result); err != nil {
			return err
		}
	}
	if !p.directoryGate.TryAdmit(tc.ID) {
		return p.stopForDirectoryInstructions(result, directoryInstructionStopReplan)
	}
	return nil
}

// The stream callback only checks frozen coverage. Resolve paths again on the
// worker before admitting a speculative operation: a directory symlink may
// have changed since the cached lookup. Discovery may warm the binding cache,
// but only formal dispatch may stop the response or publish candidate facts.
func (p toolExecutionPipeline) revalidateSpeculativeDirectoryInstructions(ctx context.Context, tc message.ToolCall) error {
	if p.directoryRuntime == nil {
		return nil
	}
	p.directoryGate = nil
	var result ToolExecutionResult
	if err := p.discoverToolDirectoryInstructions(ctx, tc, &result); err != nil {
		return errDirectorySpeculationDeferred
	}
	return nil
}

func (p toolExecutionPipeline) discoverAfterSuccessfulRead(ctx context.Context, tc message.ToolCall, observation tools.ReadObservation, result *ToolExecutionResult) {
	if p.directoryRuntime == nil || !directoryInstructionReadTool(tc.Name) {
		return
	}
	var err error
	if tc.Name == tools.NameRead {
		if observation.ActivationPath == "" {
			scope := "."
			path := observation.Path
			if path == "" {
				path = tools.ExtractReadPathFromArgsInDir(tc.Args, p.effectiveToolBaseDir())
			}
			if rel, inside := relativePathWithin(p.directoryRuntime.binding.CheckoutRoot, filepath.Dir(path)); inside {
				scope = filepath.ToSlash(rel)
			}
			d := directoryInstructionDiscovery{Binding: p.directoryRuntime.binding, CallID: tc.ID, Complete: false, Observations: map[string]directoryInstructionObservation{scope: directoryBlocked(path, message.DirectoryInstructionReasonIO, fmt.Errorf("read target identity could not be verified; read the file again before modifying it"))}}
			if p.directoryRequest != nil {
				d.RequestSeq = p.directoryRequest.RequestSeq
				d.AttemptSeq = p.directoryRequest.AttemptSeq
			}
			result.DirectoryDiscovery = &d
			err = p.stopForDirectoryInstructions(result, directoryInstructionStopBlocked)
		} else {
			err = p.discoverDirectoryInstructionTargets(ctx, tc, []tools.InstructionTarget{{Path: observation.ActivationPath, Mode: tools.InstructionTargetContent}}, result)
		}
	} else {
		err = p.discoverToolDirectoryInstructions(ctx, tc, result)
	}
	if err != nil {
		// The read succeeded. Only subsequent admission is stopped.
		result.Notes = append(result.Notes, err.Error())
	}
}
