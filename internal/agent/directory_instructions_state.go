package agent

import (
	"context"
	"sync/atomic"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type directoryInstructionBinding struct {
	SessionEpoch    uint64
	AgentID         string
	Generation      uint64
	CheckoutRoot    string
	ContentRoot     string
	WorkDir         string
	FallbackAllowed bool
}
type directoryInstructionObservation struct{ Status, SourcePath, Body, BodyHash, Reason, Detail string }
type directoryInstructionSnapshot struct {
	Observations map[string]directoryInstructionObservation
}
type directoryInstructionDiscovery struct {
	Binding      directoryInstructionBinding
	Targets      []tools.InstructionTarget
	Resolutions  map[tools.InstructionTarget]directoryInstructionTargetResolution
	Observations map[string]directoryInstructionObservation
	Complete     bool
	RequestSeq   uint64
	AttemptSeq   uint64
	CallID       string
}

// An empty Directory with no Failure means the target is outside this checkout,
// including nested checkouts. Failures are distinct from confirmed absence.
type directoryInstructionTargetResolution struct {
	Directory string
	Failure   *directoryInstructionObservation
}
type directoryInstructionVersion = message.DirectoryInstructionRef
type directoryInstructionFrozenView struct {
	Targets      map[tools.InstructionTarget]string
	Observations map[string]directoryInstructionObservation
	Revision     uint64
}
type directoryInstructionView struct {
	Initial, Dynamic, Blocked []message.DirectoryInstructionFact
	DynamicIndex              map[string]int
	Revision                  uint64
	Err                       error
}
type directoryInstructionRequest struct {
	Binding                directoryInstructionBinding
	RequestSeq, AttemptSeq uint64
	Visible                map[directoryInstructionVersion]struct{}
}
type directoryInstructionRuntime struct {
	inputBinding    directoryInstructionBinding
	readDenied      func(string) bool
	warned          map[string]struct{}
	pending         map[string]directoryInstructionObservation
	blockedRevision map[string]string
	initial         directoryInstructionSnapshot
	attempt         atomic.Pointer[directoryInstructionAttempt]
	binding         directoryInstructionBinding
	ctx             context.Context
	cancel          context.CancelFunc
	discoveryToken  chan struct{}
	observed        map[string]directoryInstructionObservation
	observedBytes   int
	frozen          atomic.Pointer[directoryInstructionFrozenView]
	view            atomic.Pointer[directoryInstructionView]
	boundaries      map[string]bool
	resolvedTargets map[tools.InstructionTarget]string
	failureRequest  uint64
	failures        map[string]directoryInstructionObservation
}

func newDirectoryInstructionRuntime(ctx context.Context, binding directoryInstructionBinding) *directoryInstructionRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &directoryInstructionRuntime{binding: binding, inputBinding: binding, ctx: ctx, cancel: cancel, discoveryToken: make(chan struct{}, 1), observed: make(map[string]directoryInstructionObservation), boundaries: make(map[string]bool), resolvedTargets: make(map[tools.InstructionTarget]string)}
	r.discoveryToken <- struct{}{}
	r.frozen.Store(&directoryInstructionFrozenView{Observations: make(map[string]directoryInstructionObservation)})
	return r
}
func directoryInstructionFactFromObservation(binding directoryInstructionBinding, scope string, o directoryInstructionObservation) message.DirectoryInstructionFact {
	f := message.DirectoryInstructionFact{CheckoutRoot: binding.CheckoutRoot, Scope: scope, SourcePath: o.SourcePath, Body: o.Body, BodyHash: o.BodyHash, Reason: o.Reason, Detail: o.Detail}
	switch o.Status {
	case "present":
		f.Operation = message.DirectoryInstructionUpsert
	case "absent":
		f.Operation = message.DirectoryInstructionRemove
	default:
		f.Operation = message.DirectoryInstructionBlocked
	}
	return f
}
