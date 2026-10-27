package agent

import (
	"context"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type directoryInstructionAttempt struct {
	request directoryInstructionRequest
}

// Directory instructions are prompt material, not harness policy: discovery
// only collects the rules that apply to a tool's target so the next request can
// inject them. It never changes a tool's outcome, cancels sibling calls, or
// admits/blocks execution — a hard constraint belongs in a permission rule or a
// hook, not in AGENTS.md prose.

func (p toolExecutionPipeline) directoryAttemptSnapshot() toolExecutionPipeline {
	if p.directoryRuntime != nil && p.directoryRequest == nil {
		if attempt := p.directoryRuntime.attempt.Load(); attempt != nil {
			p.directoryRequest = &attempt.request
		}
	}
	return p
}

func (p toolExecutionPipeline) discoverToolDirectoryInstructions(ctx context.Context, tc message.ToolCall, result *ToolExecutionResult) {
	if p.directoryRuntime == nil {
		return
	}
	targets, err := tools.InstructionTargets(tc.Name, tc.Args, p.effectiveToolBaseDir())
	if err != nil {
		return
	}
	p.discoverDirectoryInstructionTargets(ctx, tc, targets, result)
}

func (p toolExecutionPipeline) discoverDirectoryInstructionTargets(ctx context.Context, tc message.ToolCall, targets []tools.InstructionTarget, result *ToolExecutionResult) {
	if len(targets) == 0 {
		return
	}
	if p.directoryRequest != nil {
		ctx = withDirectoryInstructionDiscoveryRequest(ctx, p.directoryRequest.RequestSeq)
	}
	if p.currentRuleset != nil {
		check, identity := p.directoryInstructionReadPermission()
		ctx = context.WithValue(ctx, directoryInstructionPermissionContextKey{}, directoryInstructionPermissionOverride{check: check, identity: identity})
	}
	d, err := p.directoryRuntime.Discover(ctx, p.directoryRuntime.binding, targets)
	if err != nil {
		return
	}
	d.CallID = tc.ID
	if p.directoryRequest != nil {
		d.RequestSeq = p.directoryRequest.RequestSeq
		d.AttemptSeq = p.directoryRequest.AttemptSeq
	}
	result.DirectoryDiscovery = &d
}

// discoverAfterToolSuccess records the rules that apply to a successful call so
// the next request can inject them. The stream callback only checks frozen
// coverage, so resolve paths again on the worker: a directory symlink may have
// changed since the cached lookup.
func (p toolExecutionPipeline) discoverAfterToolSuccess(ctx context.Context, tc message.ToolCall, observation tools.ReadObservation, result *ToolExecutionResult) {
	if p.directoryRuntime == nil {
		return
	}
	if tc.Name == tools.NameRead {
		// An unstable read identity cannot name the directory that was actually
		// read, so there is nothing to attribute rules to.
		if observation.ActivationPath == "" {
			return
		}
		p.discoverDirectoryInstructionTargets(ctx, tc, []tools.InstructionTarget{{Path: observation.ActivationPath, Mode: tools.InstructionTargetContent}}, result)
		return
	}
	p.discoverToolDirectoryInstructions(ctx, tc, result)
}
