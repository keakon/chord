package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkCanonicalApplyPatchArgs(b *testing.B) {
	for _, size := range []int{64 << 10, 512 << 10} {
		patch := "*** Begin Patch\n*** Add File: sample.txt\n" + strings.Repeat("+sample content\n", size/16) + "*** End Patch"
		for _, shape := range []string{"bare", "canonical", "envelope"} {
			b.Run(fmt.Sprintf("%s/%dKiB", shape, size>>10), func(b *testing.B) {
				input := json.RawMessage(patch)
				field := "patch"
				if shape == "envelope" {
					field = "input"
				}
				if shape != "bare" {
					var err error
					input, err = json.Marshal(map[string]string{field: patch})
					if err != nil {
						b.Fatal(err)
					}
				}
				b.SetBytes(int64(len(input)))
				b.ReportAllocs()
				for b.Loop() {
					_ = canonicalApplyPatchArgs(input)
				}
			})
		}
	}
}
