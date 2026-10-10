package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
)

func TestImageDeliveryCandidateCrashWindows(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-inline", true: "published-inline"}[published], func(t *testing.T) {
			b := imageFixture(t)
			ctx, dir, _ := imageToolContext(t, "mixed-call")
			tool := &GenerateImageTool{Backend: b, BaseDir: t.TempDir()}
			id := strings.Repeat("a", 64)
			manifest := "artifact:images/operations/" + id + ".json"
			path := filepath.Join(dir, "images", "operations", id+".json")
			op := imageOperation{OperationID: id, State: imagegen.StateCompleted, Manifest: manifest, TargetFingerprint: ImageTargetFingerprint(b.target), OutputFormat: "jpeg"}
			inline := generatedImageRef(b.image, op, "A forest")
			op.Candidates = []imageCandidateReceipt{{Image: inline}, {URL: "https://example.invalid/image", Image: GeneratedImage{RevisedPrompt: "A lake"}}}
			if published {
				if _, err := SaveImageArtifact(t.Context(), dir, b.image); err != nil {
					t.Fatal(err)
				}
			}
			if err := saveImageOperation(ctx, dir, path, op); err != nil {
				t.Fatal(err)
			}

			output, err := tool.finishImageOperation(ctx, dir, path, &op, imagePublication{})
			if !published {
				if err == nil || b.downloads.Load() != 0 {
					t.Fatal("missing inline image hidden by URL recovery")
				}
				data, _ := os.ReadFile(path)
				if err := json.Unmarshal(data, &op); err != nil {
					t.Fatal(err)
				}
				if op.State == imagegen.StateSaved {
					t.Fatal("incomplete operation reported saved")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var summary ImageGenerationSummary
				if err := json.Unmarshal([]byte(output), &summary); err != nil {
					t.Fatal(err)
				}
				if len(summary.Images) != 2 || summary.Images[0].RevisedPrompt != "A forest" || summary.Images[1].RevisedPrompt != "A lake" || len(summary.Warnings) < 2 {
					t.Fatal("candidate metadata or mismatch warning lost", summary)
				}
				if repeated, err := tool.finishImageOperation(ctx, dir, path, &op, imagePublication{}); err != nil || repeated != output || b.downloads.Load() != 1 {
					t.Fatal("recovery repeated download", err)
				}
			}
			if b.calls.Load() != 0 {
				t.Fatal("recovery generated again")
			}
		})
	}
}

func TestImageDeliveryPublishedDownloadDoesNotRedownload(t *testing.T) {
	b := imageFixture(t)
	ctx, dir, _ := imageToolContext(t, "published-call")
	tool := &GenerateImageTool{Backend: b, BaseDir: t.TempDir()}
	id := strings.Repeat("b", 64)
	manifest := "artifact:images/operations/" + id + ".json"
	op := imageOperation{OperationID: id, State: imagegen.StateCompleted, Manifest: manifest, TargetFingerprint: ImageTargetFingerprint(b.target)}
	op.Candidates = []imageCandidateReceipt{{Image: generatedImageRef(b.image, op, "A tree"), URL: "https://example.invalid/image"}}
	if _, err := SaveImageArtifact(t.Context(), dir, b.image); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "images", "operations", id+".json")
	if err := saveImageOperation(ctx, dir, path, op); err != nil {
		t.Fatal(err)
	}

	if _, err := tool.finishImageOperation(ctx, dir, path, &op, imagePublication{}); err != nil || b.calls.Load() != 0 || b.downloads.Load() != 0 {
		t.Fatal("published download was not reused", err)
	}
}
