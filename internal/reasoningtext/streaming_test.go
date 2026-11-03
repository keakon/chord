package reasoningtext

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func TestStreamingHeadingsMatchesCompleteInput(t *testing.T) {
	samples := []string{
		"**First**\nBody.\n**Second**\nMore.\n\nTail.",
		"**First****Second****Third**\nText.\n**Fourth**\n",
		"Body.\n**Planning next**\n\nKeep **important** text.\n**Final**\n",
		"检查**配置**文件。\n**检查加载器**\n正文。\n**下一节**\n",
		"**First**\r\nBody.\r\n**Second**\r\n",
		"Paragraph\n\n**Long heading with `code` and symbols**\n\nTrailing **bold** text",
	}
	random := rand.New(rand.NewPCG(13, 29))
	tokens := []string{"a", ".", "*", "**", "Heading", "\n", "\n\n", "\r", " ", "`"}
	for range 100 {
		var text strings.Builder
		for range 80 {
			text.WriteString(tokens[random.IntN(len(tokens))])
		}
		samples = append(samples, text.String())
	}
	for _, sample := range samples {
		for _, step := range []int{1, 3, 17, len(sample)} {
			var normalizer StreamingHeadings
			var snapshots, copies []string
			for end := 0; end < len(sample)+step; end += step {
				input := sample[:min(end, len(sample))]
				if got, want := normalizer.Normalize(input), NormalizeStreamingHeadings(input); got != want {
					t.Fatalf("step %d input %q: got %q, want %q", step, input, got, want)
				}
				snapshots = append(snapshots, normalizer.output)
				copies = append(copies, strings.Clone(normalizer.output))
			}
			for _, replacement := range []string{"short", "", sample, "different\n**Heading**\nbody"} {
				if got, want := normalizer.Normalize(replacement), NormalizeStreamingHeadings(replacement); got != want {
					t.Fatalf("replacement %q: got %q, want %q", replacement, got, want)
				}
			}
			for i := range snapshots {
				if snapshots[i] != copies[i] {
					t.Fatalf("snapshot %d changed after append or reset", i)
				}
			}
		}
	}
}
