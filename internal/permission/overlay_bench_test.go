package permission

import (
	"fmt"
	"testing"
)

func benchmarkOverlay() *Overlay {
	o := NewOverlay()
	o.SetActiveRole("builder")
	base := make(Ruleset, 0, 64)
	for i := 0; i < 32; i++ {
		base = append(base, Rule{Permission: "Shell", Pattern: fmt.Sprintf("cmd%d *", i), Action: ActionAsk})
		base = append(base, Rule{Permission: "Read", Pattern: fmt.Sprintf("**/file%d.go", i), Action: ActionAllow})
	}
	o.SetBase(base)
	for i := 0; i < 16; i++ {
		_ = o.AddPersistentRule("builder", Rule{Permission: "Shell", Pattern: fmt.Sprintf("git status %d", i), Action: ActionAllow}, ScopeProject, "project-agent.yaml")
	}
	for i := 0; i < 8; i++ {
		o.AddSessionRule("builder", Rule{Permission: "Shell", Pattern: fmt.Sprintf("go test ./pkg%d", i), Action: ActionAllow})
	}
	return o
}

func BenchmarkOverlayEvaluate(b *testing.B) {
	o := benchmarkOverlay()
	queries := []struct {
		permission string
		pattern    string
	}{
		{"Shell", "go test ./pkg7"},
		{"Shell", "git status 8"},
		{"Read", "src/file12.go"},
		{"Shell", "unknown command"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q := queries[i&3]
		_ = o.Evaluate(q.permission, q.pattern)
	}
}

func BenchmarkOverlayEvaluateParallel(b *testing.B) {
	o := benchmarkOverlay()
	queries := []struct {
		permission string
		pattern    string
	}{
		{"Shell", "go test ./pkg7"},
		{"Shell", "git status 8"},
		{"Read", "src/file12.go"},
		{"Shell", "unknown command"},
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			q := queries[i&3]
			_ = o.Evaluate(q.permission, q.pattern)
			i++
		}
	})
}

func BenchmarkOverlayIsDisabled(b *testing.B) {
	o := benchmarkOverlay()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = o.IsDisabled("Shell")
	}
}
