package permission

import (
	"testing"

	"github.com/keakon/chord/internal/toolname"
)

// BenchmarkEvaluate_EditPatchFallback benchmarks the permission evaluation
// with edit/patch fallback logic for various ruleset sizes
func BenchmarkEvaluate_EditPatchFallback(b *testing.B) {
	benchmarks := []struct {
		name    string
		ruleset Ruleset
		perm    string
		pattern string
	}{
		{
			name: "small ruleset (5 rules)",
			ruleset: Ruleset{
				{Permission: "*", Pattern: "*", Action: ActionDeny},
				{Permission: "read", Pattern: "*", Action: ActionAllow},
				{Permission: "write", Pattern: "*.md", Action: ActionAllow},
				{Permission: "edit", Pattern: "src/*", Action: ActionAllow},
				{Permission: "shell", Pattern: "*", Action: ActionAsk},
			},
			perm:    "patch",
			pattern: "src/main.go",
		},
		{
			name: "medium ruleset (20 rules)",
			ruleset: func() Ruleset {
				rs := Ruleset{
					{Permission: "*", Pattern: "*", Action: ActionDeny},
				}
				for range 19 {
					rs = append(rs, Rule{
						Permission: "read",
						Pattern:    "*.txt",
						Action:     ActionAllow,
					})
				}
				rs = append(rs, Rule{Permission: "edit", Pattern: "src/*", Action: ActionAllow})
				return rs
			}(),
			perm:    "patch",
			pattern: "src/main.go",
		},
		{
			name: "large ruleset (50 rules)",
			ruleset: func() Ruleset {
				rs := Ruleset{
					{Permission: "*", Pattern: "*", Action: ActionDeny},
				}
				for range 48 {
					rs = append(rs, Rule{
						Permission: "read",
						Pattern:    "*.txt",
						Action:     ActionAllow,
					})
				}
				rs = append(rs, Rule{Permission: "edit", Pattern: "src/*", Action: ActionAllow})
				rs = append(rs, Rule{Permission: "write", Pattern: "*", Action: ActionAllow})
				return rs
			}(),
			perm:    "patch",
			pattern: "src/main.go",
		},
		{
			name: "fallback hit early (edit/patch at start)",
			ruleset: Ruleset{
				{Permission: "*", Pattern: "*", Action: ActionDeny},
				{Permission: "edit", Pattern: "src/*", Action: ActionAllow},
				{Permission: "read", Pattern: "*", Action: ActionAllow},
				{Permission: "write", Pattern: "*", Action: ActionAllow},
			},
			perm:    "patch",
			pattern: "src/main.go",
		},
		{
			name: "fallback hit late (edit/patch at end)",
			ruleset: Ruleset{
				{Permission: "*", Pattern: "*", Action: ActionDeny},
				{Permission: "read", Pattern: "*", Action: ActionAllow},
				{Permission: "write", Pattern: "*", Action: ActionAllow},
				{Permission: "shell", Pattern: "*", Action: ActionAllow},
				{Permission: "edit", Pattern: "src/*", Action: ActionAllow},
			},
			perm:    "patch",
			pattern: "src/main.go",
		},
		{
			name: "no fallback needed (patch rule exists)",
			ruleset: Ruleset{
				{Permission: "*", Pattern: "*", Action: ActionDeny},
				{Permission: "patch", Pattern: "src/*", Action: ActionAllow},
				{Permission: "read", Pattern: "*", Action: ActionAllow},
			},
			perm:    "patch",
			pattern: "src/main.go",
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = bm.ruleset.Evaluate(bm.perm, bm.pattern)
			}
		})
	}
}

// BenchmarkIsDisabled_EditPatchFallback benchmarks IsDisabled with
// edit/patch fallback for various ruleset sizes
func BenchmarkIsDisabled_EditPatchFallback(b *testing.B) {
	benchmarks := []struct {
		name     string
		ruleset  Ruleset
		toolName string
	}{
		{
			name: "small ruleset (5 rules)",
			ruleset: Ruleset{
				{Permission: "*", Pattern: "*", Action: ActionAllow},
				{Permission: "read", Pattern: "*", Action: ActionAllow},
				{Permission: "write", Pattern: "*", Action: ActionAllow},
				{Permission: "edit", Pattern: "*", Action: ActionDeny},
				{Permission: "shell", Pattern: "*", Action: ActionAllow},
			},
			toolName: "patch",
		},
		{
			name: "medium ruleset (20 rules)",
			ruleset: func() Ruleset {
				rs := Ruleset{
					{Permission: "*", Pattern: "*", Action: ActionAllow},
				}
				for range 18 {
					rs = append(rs, Rule{
						Permission: "read",
						Pattern:    "*",
						Action:     ActionAllow,
					})
				}
				rs = append(rs, Rule{Permission: "edit", Pattern: "*", Action: ActionDeny})
				return rs
			}(),
			toolName: "patch",
		},
		{
			name: "large ruleset (50 rules)",
			ruleset: func() Ruleset {
				rs := Ruleset{
					{Permission: "*", Pattern: "*", Action: ActionAllow},
				}
				for range 48 {
					rs = append(rs, Rule{
						Permission: "read",
						Pattern:    "*",
						Action:     ActionAllow,
					})
				}
				rs = append(rs, Rule{Permission: "edit", Pattern: "*", Action: ActionDeny})
				return rs
			}(),
			toolName: "patch",
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = bm.ruleset.IsDisabled(bm.toolName)
			}
		})
	}
}

// BenchmarkEvaluate_NoFallback benchmarks standard permission evaluation
// without edit/patch fallback (baseline comparison)
func BenchmarkEvaluate_NoFallback(b *testing.B) {
	benchmarks := []struct {
		name    string
		ruleset Ruleset
		perm    string
		pattern string
	}{
		{
			name: "small ruleset (5 rules)",
			ruleset: Ruleset{
				{Permission: "*", Pattern: "*", Action: ActionDeny},
				{Permission: "read", Pattern: "*", Action: ActionAllow},
				{Permission: "write", Pattern: "*.md", Action: ActionAllow},
				{Permission: "shell", Pattern: "*", Action: ActionAsk},
				{Permission: "grep", Pattern: "*", Action: ActionAllow},
			},
			perm:    "read",
			pattern: "file.txt",
		},
		{
			name: "medium ruleset (20 rules)",
			ruleset: func() Ruleset {
				rs := Ruleset{
					{Permission: "*", Pattern: "*", Action: ActionDeny},
				}
				for range 18 {
					rs = append(rs, Rule{
						Permission: "write",
						Pattern:    "*.txt",
						Action:     ActionAllow,
					})
				}
				rs = append(rs, Rule{Permission: "read", Pattern: "*", Action: ActionAllow})
				return rs
			}(),
			perm:    "read",
			pattern: "file.txt",
		},
		{
			name: "large ruleset (50 rules)",
			ruleset: func() Ruleset {
				rs := Ruleset{
					{Permission: "*", Pattern: "*", Action: ActionDeny},
				}
				for range 48 {
					rs = append(rs, Rule{
						Permission: "write",
						Pattern:    "*.txt",
						Action:     ActionAllow,
					})
				}
				rs = append(rs, Rule{Permission: "read", Pattern: "*", Action: ActionAllow})
				return rs
			}(),
			perm:    "read",
			pattern: "file.txt",
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = bm.ruleset.Evaluate(bm.perm, bm.pattern)
			}
		})
	}
}

// BenchmarkNormalize benchmarks the toolname normalization function
// used in fallback logic
func BenchmarkNormalize(b *testing.B) {
	names := []string{"edit", "EDIT", "Edit", "patch", "PATCH", "Patch", "read", "Write", "SHELL"}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = toolname.Normalize(names[i%len(names)])
	}
}
