package tools

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
)

type ImageGenerationBackend interface {
	Target() imagegen.Target
	Timeout() time.Duration
	Run(context.Context, imagegen.Request, func() error) (*imagegen.Result, error)
	Download(context.Context, string) (imagegen.Image, error)
	Check(context.Context, imagegen.Request) error
}

type GenerateImageTool struct {
	Backend ImageGenerationBackend
	BaseDir string
}

func (*GenerateImageTool) Name() string          { return NameGenerateImage }
func (*GenerateImageTool) StrictArguments() bool { return true }

func (*GenerateImageTool) IsReadOnly() bool              { return false }
func (t *GenerateImageTool) IsAvailable() bool           { return t.Backend != nil }
func (t *GenerateImageTool) WithBaseDir(dir string) Tool { c := *t; c.BaseDir = dir; return &c }

type GeneratedImage struct {
	ArtifactRef
	Reference     string `json:"reference"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type ImageGenerationSummary struct {
	OperationID  string           `json:"operation_id"`
	State        string           `json:"state"`
	Images       []GeneratedImage `json:"images,omitempty"`
	Manifest     string           `json:"manifest"`
	OutputPath   string           `json:"output_path,omitempty"`
	RequestID    string           `json:"request_id,omitempty"`
	Usage        map[string]any   `json:"usage,omitempty"`
	BillingState string           `json:"billing_state"`
	Warnings     []string         `json:"warnings,omitempty"`
}

type imageOperation struct {
	ImageGenerationSummary
	RequestHash       string                   `json:"request_hash"`
	AgentID           string                   `json:"agent_id"`
	TaskID            string                   `json:"task_id,omitempty"`
	TurnID            uint64                   `json:"turn_id"`
	Target            string                   `json:"target"`
	TargetFingerprint string                   `json:"target_fingerprint"`
	Sources           []string                 `json:"source_sha256,omitempty"`
	Candidates        []imageCandidateReceipt  `json:"candidates,omitempty"`
	OutputFormat      string                   `json:"output_format,omitempty"`
	Background        string                   `json:"background,omitempty"`
	Failure           *imagegen.FailureDetails `json:"failure,omitempty"`
}

func saveImageOperation(ctx context.Context, dir, path string, op imageOperation) error {
	return imagegen.RetryDelivery(ctx, "save-receipt", func() error { return writeImageOperation(dir, path, op) })
}

func writeImageOperation(dir, path string, op imageOperation) error {
	data, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("encode image operation: %w", err)
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("image operation metadata exceeds limit")
	}
	parent := filepath.Dir(path)
	if err := privatefs.EnsureDir(dir, parent); err != nil {
		return fmt.Errorf("prepare image operation directory: %w", err)
	}
	root, err := openImageDirectory(dir, parent)
	if err != nil {
		return err
	}
	defer root.Close()
	temp := ".receipt-" + hex.EncodeToString(randomImageID())
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privatefs.FileMode)
	if err != nil {
		return fmt.Errorf("create image receipt: %w", err)
	}
	defer root.Remove(temp)
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist image receipt: %w", err)
	}
	if err := root.Rename(temp, filepath.Base(path)); err != nil {
		return fmt.Errorf("publish image operation: %w", err)
	}
	d, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open image receipt directory: %w", err)
	}
	defer d.Close()
	return privatefs.SyncDirectory(d)
}

func DecodeImageGenerationRequest(raw json.RawMessage) (imagegen.Request, error) {
	var r imagegen.Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, fmt.Errorf("invalid image arguments: %w", err)
	}
	return r, nil
}

func (t *GenerateImageTool) Execute(ctx context.Context, raw json.RawMessage) (output string, err error) {
	if !t.IsAvailable() {
		return "", fmt.Errorf("image generation is not configured")
	}
	if _, _, _, err := SanitizeUnknownArgsWithDiagnostics(t, raw); err != nil {
		return "", err
	}
	r, err := DecodeImageGenerationRequest(raw)
	if err != nil {
		return "", err
	}
	if selector, ok := t.Backend.(interface {
		Prepare(context.Context, imagegen.Request) (ImageGenerationBackend, error)
	}); ok {
		backend, err := selector.Prepare(ctx, r)
		if err != nil {
			return "", err
		}
		local := *t
		local.Backend = backend
		t = &local
	}
	if err := t.Backend.Target().Validate(&r); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, t.Backend.Timeout())
	defer cancel()
	if err := t.Backend.Check(ctx, r); err != nil {
		return "", err
	}
	sessionDir := SessionDirFromContext(ctx)
	if sessionDir == "" {
		return "", fmt.Errorf("image generation requires a persistent session")
	}
	// Prepare every input and delivery path before sending a paid request.
	var sources []string
	var referenceBytes int
	for _, ref := range r.ReferenceImages {
		path, err := ResolveImageArtifactPath(sessionDir, ref, t.BaseDir)
		if err != nil {
			return "", err
		}
		base := t.BaseDir
		if strings.HasPrefix(ref, ImageArtifactPrefix) {
			base = sessionDir
		}
		f, err := openImageSnapshot(base, path)
		if err != nil {
			return "", fmt.Errorf("open reference image: %w", err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return "", fmt.Errorf("inspect reference image: %w", err)
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			return "", fmt.Errorf("reference image must be a regular file")
		}
		data, err := imagegen.ReadBounded(f, imagegen.MaxImageBytes)
		_ = f.Close()
		if err != nil {
			return "", err
		}
		img, err := imagegen.ValidateImage(ctx, data, "")
		if err != nil {
			return "", err
		}
		referenceBytes += len(data)
		if referenceBytes > 20<<20 {
			return "", fmt.Errorf("reference images exceed the 20 MiB aggregate upload limit")
		}
		r.References = append(r.References, img)
		sources = append(sources, imageDigest(data))
	}
	var outputRoot *os.Root
	var outputName, outputAbs string
	var outputPreflightErr error
	if r.OutputPath != "" {
		outputAbs, err = resolveToolPathAbsInDir(r.OutputPath, t.BaseDir)
		if err != nil {
			return "", err
		}
		cwd, err := filepath.Abs(t.BaseDir)
		if err != nil {
			return "", fmt.Errorf("resolve image workspace: %w", err)
		}
		rel, err := filepath.Rel(cwd, outputAbs)
		if err != nil || !filepath.IsLocal(rel) {
			return "", fmt.Errorf("output_path must be within the workspace")
		}
		outputRoot, err = openImageDirectory(cwd, filepath.Dir(outputAbs))
		if err != nil {
			return "", fmt.Errorf("open output directory: %w", err)
		}
		defer outputRoot.Close()
		outputName = filepath.Base(rel)
		if _, err := outputRoot.Lstat(outputName); err == nil {
			outputPreflightErr = fmt.Errorf("output_path already exists")
		} else if !os.IsNotExist(err) {
			outputPreflightErr = fmt.Errorf("inspect output_path: %w", err)
		}
	}
	id := ToolCallIDFromContext(ctx)
	if id == "" {
		id = hex.EncodeToString(randomImageID())
	}
	owner := TaskIDFromContext(ctx)
	if owner == "" {
		owner = identity.MainAgentID
	}
	id = imageDigest([]byte(owner + "\x00" + id))
	rel := filepath.ToSlash(filepath.Join("images", "operations", id+".json"))
	path := filepath.Join(sessionDir, filepath.FromSlash(rel))
	target := t.Backend.Target()
	requestBytes, _ := json.Marshal(struct {
		Request imagegen.Request
		Sources []string
	}{r, sources})
	op := imageOperation{OperationID: id, State: imagegen.StateNotSent, Manifest: ImageArtifactPrefix + rel, BillingState: "unknown", RequestHash: imageDigest(requestBytes), AgentID: AgentIDFromContext(ctx), TaskID: TaskIDFromContext(ctx), TurnID: TurnIDFromContext(ctx), Target: target.Provider + "/" + target.Model, TargetFingerprint: ImageTargetFingerprint(target), Sources: sources}
	lock, err := lockImageOperation(sessionDir, path)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if data, readErr := os.ReadFile(path); readErr == nil {
		var previous imageOperation
		if err := json.Unmarshal(data, &previous); err != nil {
			return "", fmt.Errorf("read existing image operation: %w", err)
		}
		if previous.RequestHash != op.RequestHash {
			return "", fmt.Errorf("image operation ID is already bound to another request")
		}
		return t.finishImageOperation(ctx, sessionDir, path, &previous, imagePublication{root: outputRoot, name: outputName, path: outputAbs, restored: true})
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("read image operation: %w", readErr)
	}
	if outputPreflightErr != nil {
		return "", outputPreflightErr
	}
	claim, err := privatefs.OpenFile(sessionDir, path+".claim", os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", fmt.Errorf("image operation is already claimed; do not regenerate: %w", err)
	}
	if err := claim.Close(); err != nil {
		return "", fmt.Errorf("close image operation claim: %w", err)
	}
	if err := saveImageOperation(ctx, sessionDir, path, op); err != nil {
		return "", err
	}
	result, runErr := t.Backend.Run(ctx, r, func() error {
		actual := t.Backend.Target()
		op.Target = actual.Provider + "/" + actual.Model
		op.TargetFingerprint = ImageTargetFingerprint(actual)
		op.State = imagegen.StateUnknown
		return saveImageOperation(ctx, sessionDir, path, op)
	})
	if runErr != nil {
		if failure, ok := errors.AsType[*imagegen.Failure](runErr); ok {
			op.State = failure.State
			op.RequestID = failure.RequestID
			details := failure.Details
			op.Failure = &details
		}
		if err := saveImageOperation(ctx, sessionDir, path, op); err != nil {
			return "", fmt.Errorf("operation %s; persist failure evidence: %w (request must not be replayed)", id, err)
		}
		return "", fmt.Errorf("operation %s, manifest %s: %w", id, op.Manifest, runErr)
	}
	op.State = imagegen.StateCompleted
	defer func() {
		if err != nil {
			if _, classified := errors.AsType[*imageDeliveryFailure](err); !classified {
				err = imageDeliveryError(op, "save or publish image result", err)
			}
		}
	}()
	op.RequestID = result.RequestID
	op.Usage = result.Usage
	op.Warnings = result.Warnings
	op.OutputFormat, op.Background = r.OutputFormat, r.Background
	op.Candidates = make([]imageCandidateReceipt, len(result.Images))
	for i, candidate := range result.Images {
		receipt := imageCandidateReceipt{URL: candidate.URL, Image: GeneratedImage{RevisedPrompt: candidate.RevisedPrompt}}
		if len(candidate.Data) > 0 {
			receipt.Image = generatedImageRef(candidate.Image, op, candidate.RevisedPrompt)
		}
		op.Candidates[i] = receipt
	}
	if err := saveImageOperation(ctx, sessionDir, path, op); err != nil {
		return "", imageDeliveryError(op, "persist receipt", err)
	}
	// Publish inline originals under the identities already committed in the
	// receipt. A repeated durable call reuses them without generating again.
	for i, candidate := range result.Images {
		if len(candidate.Data) == 0 {
			continue
		}
		if _, err := SaveImageArtifact(ctx, sessionDir, candidate.Image); err != nil {
			return "", imageDeliveryError(op, "save original", err)
		}
		op.Candidates[i].Saved = true
		if err := saveImageOperation(ctx, sessionDir, path, op); err != nil {
			return "", err
		}
	}
	return t.finishImageOperation(ctx, sessionDir, path, &op, imagePublication{root: outputRoot, name: outputName, path: outputAbs})
}

func (t *GenerateImageTool) imageSummary(ctx context.Context, op imageOperation, sessionDir string) (string, error) {
	if len(op.Images) == 0 || len(op.Images) > imagegen.MaxImages {
		return "", fmt.Errorf("saved operation has no valid image collection")
	}
	for _, img := range op.Images {
		original, err := ReadGeneratedOriginal(ctx, sessionDir, img.Reference)
		if err != nil {
			return "", fmt.Errorf("saved image is missing or changed: %w", err)
		}
		if img.MimeType != original.MIME || img.Width != original.Width || img.Height != original.Height || img.SizeBytes != int64(len(original.Data)) || img.SHA256 != imageDigest(original.Data) {
			return "", fmt.Errorf("saved image metadata does not match original")
		}
	}
	summary := op.ImageGenerationSummary
	summary.Images = append([]GeneratedImage(nil), op.Images...)
	data, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("encode image summary: %w", err)
	}
	// The summary is immutable before Execute can publish success. Canonical
	// transcript persistence continues through the normal tool pipeline.
	if err := imagegen.RetryDelivery(ctx, "persist-result", func() error {
		_, _, err := SaveImmutableResult(sessionDir, "image_generation", data)
		return err
	}); err != nil {
		return "", imageDeliveryError(op, "persist result", err)
	}
	if sink, ok := ImageSinkFromContext(ctx); ok {
		for _, img := range op.Images {
			sink.AddImage(message.ContentPart{Type: message.ContentPartImage, MimeType: img.MimeType, ImagePath: filepath.Join(sessionDir, filepath.FromSlash(img.RelPath)), FileName: filepath.Base(img.RelPath), DataBytes: img.SizeBytes, ArtifactID: img.ID})
		}
	}
	return string(data), nil
}
