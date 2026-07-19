package tools

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPatchLargeFile(b *testing.B) {
	// Simulate a large file similar to internal/agent/main.go (around 3000 lines)
	var lines []string
	for i := range 750 {
		lines = append(lines,
			fmt.Sprintf("func handler%d(evt Event) {", i),
			"    ctx := context.Background()",
			"    log.Info(\"processing event\")",
			"    return processEvent(ctx, evt)",
			"}",
			"",
		)
	}
	content := strings.Join(lines, "\n")

	// Create a patch that needs to search through most of the file
	// (match is near the end, requires scanning through many lines)
	patch := `@@
 func handler700(evt Event) {
     ctx := context.Background()
-    log.Info("processing event")
+    log.Debug("processing event with details")
     return processEvent(ctx, evt)
 }
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := ParsePatch("test.go", patch)
		if err != nil {
			b.Fatal(err)
		}
		_, err = applyParsedPatch(content, parsed)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPatchMultipleHunks(b *testing.B) {
	// Simulate a file with 1000 lines
	var lines []string
	for i := range 200 {
		lines = append(lines,
			fmt.Sprintf("func process%d() error {", i),
			"    x := initialize()",
			"    if err := validate(x); err != nil {",
			"        return err",
			"    }",
			"    return nil",
			"}",
		)
	}
	content := strings.Join(lines, "\n")

	// Create a patch with 2 hunks at different positions
	patch := `@@
 func process50() error {
-    x := initialize()
+    x := initializeWithConfig()
     if err := validate(x); err != nil {
@@
 func process150() error {
     x := initialize()
     if err := validate(x); err != nil {
-        return err
+        return fmt.Errorf("validation failed: %w", err)
     }
     return nil
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := ParsePatch("test.go", patch)
		if err != nil {
			b.Fatal(err)
		}
		_, err = applyParsedPatch(content, parsed)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark the worst case: patch needs all 4 layers to match
func BenchmarkPatchWithWhitespaceNormalization(b *testing.B) {
	var lines []string
	for range 500 {
		lines = append(lines,
			"func example() {",
			"    value := 123  ", // trailing whitespace
			"    return value",
			"}",
		)
	}
	content := strings.Join(lines, "\n")

	// Patch without trailing whitespace (requires layer 2 to match)
	patch := `@@
 func example() {
-    value := 123
+    value := 456
     return value
 }
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := ParsePatch("test.go", patch)
		if err != nil {
			b.Fatal(err)
		}
		_, err = applyParsedPatch(content, parsed)
		if err != nil {
			b.Fatal(err)
		}
	}
}
