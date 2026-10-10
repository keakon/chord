package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/imagegen"
)

func ImageTargetFingerprint(target imagegen.Target) string {
	data, _ := json.Marshal(target)
	return imageDigest(data)
}

// finishImageOperation is internal to the same durable tool call. It never
// sends a generation request, including when the receipt is incomplete.
func (t *GenerateImageTool) finishImageOperation(ctx context.Context, dir, path string, op *imageOperation, output imagePublication) (string, error) {
	if op.OperationID+".json" != filepath.Base(path) || len(op.Images) > imagegen.MaxImages || len(op.Candidates) > imagegen.MaxImages {
		return "", fmt.Errorf("invalid image operation receipt; do not regenerate")
	}
	if op.State == imagegen.StateSaved {
		result, err := t.imageSummary(ctx, *op, dir)
		if err != nil {
			return "", imageDeliveryError(*op, "verify saved originals", err)
		}
		return result, nil
	}
	if op.State != imagegen.StateCompleted {
		return "", fmt.Errorf("image operation is %s; no confirmed image receipt; do not regenerate", op.State)
	}
	if op.TargetFingerprint != ImageTargetFingerprint(t.Backend.Target()) {
		selector, ok := t.Backend.(interface {
			Restore(context.Context, string) (ImageGenerationBackend, error)
		})
		if !ok {
			return "", imageDeliveryError(*op, "locate original image target", fmt.Errorf("image target changed"))
		}
		backend, err := selector.Restore(ctx, op.TargetFingerprint)
		if err != nil {
			return "", imageDeliveryError(*op, "locate original image target", err)
		}
		local := *t
		local.Backend = backend
		t = &local
	}
	if err := t.completeImageCandidates(ctx, dir, path, op); err != nil {
		return "", imageDeliveryError(*op, "download or save originals", err)
	}
	if output.root != nil {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(op.Images[0].RelPath)))
		if err == nil {
			err = t.Backend.Check(ctx, imagegen.Request{OutputPath: output.path})
		}
		if err == nil {
			err = publishImageFile(ctx, output.root, output.name, data)
			if errors.Is(err, os.ErrExist) && output.restored {
				err = verifyExistingImageFile(output.root, output.name, data)
			}
			if err == nil {
				err = verifyImagePublication(t.BaseDir, output.path, output.root, output.name)
			}
		}
		if err != nil {
			op.Warnings = append(op.Warnings, "Image saved in session; output_path publication failed: "+err.Error())
		} else {
			op.OutputPath = output.path
		}
	}
	op.State = imagegen.StateSaved
	if err := saveImageOperation(ctx, dir, path, *op); err != nil {
		return "", imageDeliveryError(*op, "persist saved image manifest", err)
	}
	return t.imageSummary(ctx, *op, dir)
}

func imageDeliveryError(op imageOperation, stage string, err error) error {
	return &imageDeliveryFailure{operationID: op.OperationID, stage: stage, cause: err}
}

type imageDeliveryFailure struct {
	operationID string
	stage       string
	cause       error
}

func (e *imageDeliveryFailure) Error() string {
	return fmt.Sprintf("image generated; %s could not complete for operation %s; do not regenerate: %v", e.stage, e.operationID, e.cause)
}

func (e *imageDeliveryFailure) Unwrap() error { return e.cause }

type imagePublication struct {
	root     *os.Root
	name     string
	path     string
	restored bool
}
