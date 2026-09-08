package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Surface kinds name the role each fingerprint plays at the fallback boundary:
// requestSurfacePrimary is the surface the main provider saw and
// requestSurfaceFallback the surface actually sent to the fallback after any
// rebuild. They label the boundary log line and the reuse counters only — the
// rebuild decision itself is fallbackRequiresFreshAdmission on the boundary
// payload, never a comparison of these fingerprints.
const (
	requestSurfacePrimary  = "primary"
	requestSurfaceFallback = "fallback"
)

// requestSurfaceFingerprint describes one side of the fallback boundary: the
// messages, the model identity and the input budget of a request the boundary
// considered, plus the token estimate of the messages it actually observed —
// after the pending user queue was folded in, which is not the number the
// primary request was admitted on. Both sides are rendered into the boundary's
// log line so a rebuild can be explained; the rebuild decision itself is
// fallbackRequiresFreshAdmission on the boundary payload, and nothing here is
// persisted.
//
// The tool contract is deliberately absent. A fallback runs the same tool
// surface as the primary — the model changes, the registry does not — so a
// hash of it would be a dimension that can never differ.
type requestSurfaceFingerprint struct {
	Kind            string
	ModelRef        string
	Messages        int
	EstimatedTokens int
	// InputBudget is the effective input limit this surface was admitted
	// against. A fallback with a smaller budget may need more reduction even
	// when every other field matches.
	InputBudget int
}

func newRequestSurfaceFingerprint(kind, modelRef string, messages []message.Message, estimatedTokens, inputBudget int) requestSurfaceFingerprint {
	return requestSurfaceFingerprint{
		Kind:            kind,
		ModelRef:        modelRef,
		Messages:        len(messages),
		EstimatedTokens: estimatedTokens,
		InputBudget:     inputBudget,
	}
}

func (f requestSurfaceFingerprint) String() string {
	return fmt.Sprintf("kind=%s model=%s messages=%d estimated_tokens=%d input_budget=%d",
		f.Kind, f.ModelRef, f.Messages, f.EstimatedTokens, f.InputBudget)
}

// surfaceDifferences lists the fields on which two surfaces disagree. It feeds
// the boundary log line so a rebuild can be explained; it never decides one —
// the decision reads the boundary payload, not a pair of these fingerprints.
func (f requestSurfaceFingerprint) surfaceDifferences(other requestSurfaceFingerprint) []string {
	var diffs []string
	if f.ModelRef != other.ModelRef {
		diffs = append(diffs, "model")
	}
	if f.Messages != other.Messages {
		diffs = append(diffs, "messages")
	}
	if f.InputBudget != other.InputBudget {
		diffs = append(diffs, "input_budget")
	}
	if f.EstimatedTokens != other.EstimatedTokens {
		diffs = append(diffs, "estimated_tokens")
	}
	return diffs
}

// noteFallbackSurfaceDecision records whether a fallback boundary reused the
// prepared surface or rebuilt it. The ratio is the missing half of the
// admission story: without it a rebuild that fires on every request is
// indistinguishable from one that never fires, and both look like "fallback
// works". FallbackSurfaceCounts reports it on the per-request debug log line.
func (a *MainAgent) noteFallbackSurfaceDecision(rebuilt bool) {
	if a == nil {
		return
	}
	if rebuilt {
		a.fallbackSurfaceRebuilds.Add(1)
		return
	}
	a.fallbackSurfaceReuses.Add(1)
}

// FallbackSurfaceCounts reports how often fallback boundaries rebuilt versus
// reused the prepared request surface in this session. It is read by the
// per-request debug log line in callLLM.
func (a *MainAgent) FallbackSurfaceCounts() (rebuilt, reused int64) {
	if a == nil {
		return 0, 0
	}
	return a.fallbackSurfaceRebuilds.Load(), a.fallbackSurfaceReuses.Load()
}

// describeSurfaceDecision renders the log line for one boundary decision.
func describeSurfaceDecision(primary, target requestSurfaceFingerprint, rebuilt bool) string {
	action := "reused"
	if rebuilt {
		action = "rebuilt"
	}
	diffs := primary.surfaceDifferences(target)
	reason := "identical"
	if len(diffs) > 0 {
		reason = strings.Join(diffs, ",")
	}
	return fmt.Sprintf("surface %s primary(%s) target(%s) differs=%s", action, primary, target, reason)
}
