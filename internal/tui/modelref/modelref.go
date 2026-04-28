package modelref

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/config"
)

// SplitRunningModelRef splits a running model ref into provider, model id (may contain
// further '/'), and optional variant (inline @suffix on the model segment).
func SplitRunningModelRef(ref string) (provider, model, variant string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", ""
	}
	i := strings.Index(ref, "/")
	if i < 0 {
		m, v := config.ParseModelRef(ref)
		return "", m, v
	}
	provider = ref[:i]
	rest := ref[i+1:]
	m, v := config.ParseModelRef(rest)
	return provider, m, v
}

func joinRunningModelRef(provider, model, variant string) string {
	var b strings.Builder
	if provider != "" {
		b.WriteString(provider)
		b.WriteByte('/')
	}
	b.WriteString(model)
	if variant != "" {
		b.WriteByte('@')
		b.WriteString(variant)
	}
	return b.String()
}

func runningModelRefDisplayWidth(provider, model, variant string) int {
	return runewidth.StringWidth(joinRunningModelRef(provider, model, variant))
}

// stripFirstModelPathSegment removes the first '/'-delimited segment from model id
// (e.g. anthropic/claude-opus-4.6 → claude-opus-4.6).
func stripFirstModelPathSegment(model string) (newModel string, ok bool) {
	i := strings.Index(model, "/")
	if i < 0 {
		return model, false
	}
	return model[i+1:], true
}

// EnsureRefShowsVariant returns ref unchanged if it already has an inline @variant;
// otherwise appends @activeVariant when non-empty. RunningModelRef is often provider/model
// without @ while the client holds a separate ActiveVariant (e.g. qt/gpt-5.5 + xhigh).
func EnsureRefShowsVariant(ref, activeVariant string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ref
	}
	if _, v := config.ParseModelRef(ref); v != "" {
		return ref
	}
	if rv := strings.TrimSpace(activeVariant); rv != "" {
		return ref + "@" + rv
	}
	return ref
}

// EnsureRefShowsMatchingVariant appends activeVariant only when runningRef and selectedRef
// refer to the same provider/model base ref. This prevents a primary model's active variant
// from leaking onto a fallback running model.
func EnsureRefShowsMatchingVariant(runningRef, selectedRef, activeVariant string) string {
	runningRef = strings.TrimSpace(runningRef)
	selectedRef = strings.TrimSpace(selectedRef)
	activeVariant = strings.TrimSpace(activeVariant)
	if runningRef == "" || activeVariant == "" {
		return runningRef
	}
	_, _, runningVariant := SplitRunningModelRef(runningRef)
	if runningVariant != "" {
		return runningRef
	}
	rp, rm, _ := SplitRunningModelRef(runningRef)
	sp, sm, _ := SplitRunningModelRef(selectedRef)
	if rp == "" || rm == "" || sp == "" || sm == "" {
		return runningRef
	}
	if rp != sp || rm != sm {
		return runningRef
	}
	return runningRef + "@" + activeVariant
}

// EnsureRefShowsProvider backfills provider from selectedRef when runningRef
// temporarily loses provider prefix (e.g. "gpt-5.5@xhigh" vs "qt/gpt-5.5@xhigh").
func EnsureRefShowsProvider(runningRef, selectedRef string) string {
	runningRef = strings.TrimSpace(runningRef)
	if runningRef == "" {
		return runningRef
	}
	if strings.Contains(runningRef, "/") {
		return runningRef
	}
	p, _, _ := SplitRunningModelRef(strings.TrimSpace(selectedRef))
	if p == "" {
		return runningRef
	}
	return p + "/" + runningRef
}

// FormatRunningModelRefForDisplay backfills provider and matching variant from
// selectedRef/activeVariant, then truncates to maxLen display columns when needed.
func FormatRunningModelRefForDisplay(runningRef, selectedRef, activeVariant string, maxLen int) string {
	ref := EnsureRefShowsProvider(runningRef, selectedRef)
	ref = EnsureRefShowsMatchingVariant(ref, selectedRef, activeVariant)
	if runewidth.StringWidth(ref) > maxLen {
		ref = TruncateRunningModelRef(ref, maxLen)
	}
	return ref
}

// TruncateRunningModelRef returns ref unchanged if it already fits in maxLen display columns.
// Otherwise it simplifies until it fits (runewidth-aware): it strips leading model path segments
// first (after provider/), one segment per step.
// After no more model slashes remain, order depends on whether the original model id contained '/':
//   - Multi-segment model (e.g. anthropic/claude…): drop @variant, then drop provider.
//   - Single-segment model (e.g. gpt-5.5): drop @variant, then provider — keeps provider/model visible first.
//
// Finally, if still too wide, truncate the joined string from the end with "...".
func TruncateRunningModelRef(ref string, maxLen int) string {
	ref = strings.TrimSpace(ref)
	if idx := strings.IndexByte(ref, '\n'); idx >= 0 {
		ref = ref[:idx]
	}
	ref = strings.TrimSpace(ref)
	if maxLen <= 0 {
		maxLen = 20
	}
	if ref == "" || runewidth.StringWidth(ref) <= maxLen {
		return ref
	}

	provider, model, variant := SplitRunningModelRef(ref)
	modelHadPath := strings.Contains(model, "/")

	const maxIters = 4096
	for i := 0; i < maxIters && runningModelRefDisplayWidth(provider, model, variant) > maxLen; i++ {
		if next, ok := stripFirstModelPathSegment(model); ok {
			model = next
			continue
		}
		if modelHadPath {
			if variant != "" {
				variant = ""
				continue
			}
			if provider != "" {
				provider = ""
				continue
			}
		} else {
			if variant != "" {
				variant = ""
				continue
			}
			if provider != "" {
				provider = ""
				continue
			}
		}
		break
	}

	out := joinRunningModelRef(provider, model, variant)
	if runewidth.StringWidth(out) <= maxLen {
		return out
	}
	if maxLen > 3 {
		return runewidth.Truncate(out, maxLen, "...")
	}
	return runewidth.Truncate(out, maxLen, "")
}
