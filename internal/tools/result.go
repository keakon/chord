package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/privatefs"
)

const (
	maxImmutableResultBytes = 10 * 1024 * 1024
	MaxInlineResultBytes    = 32 * 1024
)

type SaveResultTool struct{}

func (SaveResultTool) Name() string { return NameSaveResult }

func (SaveResultTool) Description() string {
	return "Save a JSON object as an immutable content-addressed task result. Returns a ResultRef with result_type, session-relative path, SHA-256, and size. The same content safely reuses the same immutable object; existing content is never appended or overwritten."
}

func (SaveResultTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result_type": map[string]any{"type": "string", "description": "Non-empty application-defined result media type."},
			"result":      map[string]any{"type": "object", "description": "JSON object to persist without runtime interpretation."},
		},
		"required": []string{"result_type", "result"}, "additionalProperties": false,
	}
}

func (SaveResultTool) IsReadOnly() bool { return false }

func (SaveResultTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ResultType string          `json:"result_type"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	ref, _, err := SaveImmutableResult(SessionDirFromContext(ctx), args.ResultType, args.Result)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(ref)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func canonicalResultObject(resultType string, raw json.RawMessage, maxBytes int) (string, []byte, error) {
	resultType = strings.TrimSpace(resultType)
	if resultType == "" {
		return "", nil, fmt.Errorf("result_type is required")
	}
	if len(resultType) > 256 {
		return "", nil, fmt.Errorf("result_type exceeds maximum size 256 bytes")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", nil, fmt.Errorf("result must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return "", nil, fmt.Errorf("result must be a JSON object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return "", nil, fmt.Errorf("canonicalize result: %w", err)
	}
	canonical = append(canonical, '\n')
	if maxBytes > 0 && len(canonical) > maxBytes {
		return "", nil, fmt.Errorf("result exceeds maximum size %d bytes", maxBytes)
	}
	return resultType, canonical, nil
}

func resultRefID(resultType, digest string) string {
	typeSum := sha256.Sum256([]byte(resultType + "\x00" + digest))
	return "sha256-" + hex.EncodeToString(typeSum[:])
}

func SaveImmutableResult(sessionDir, resultType string, raw json.RawMessage) (ResultRef, json.RawMessage, error) {
	resultType, canonical, err := canonicalResultObject(resultType, raw, maxImmutableResultBytes)
	if err != nil {
		return ResultRef{}, nil, err
	}
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ResultRef{}, nil, fmt.Errorf("session directory is unavailable")
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	id := resultRefID(resultType, digest)
	relPath := filepath.ToSlash(filepath.Join("artifacts", "results", id+".json"))
	abs := filepath.Join(sessionDir, filepath.FromSlash(relPath))
	f, err := privatefs.OpenFile(sessionDir, abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if !os.IsExist(err) {
			return ResultRef{}, nil, err
		}
		existingDigest, hashErr := fileSHA256(abs)
		if hashErr != nil || existingDigest != digest {
			return ResultRef{}, nil, fmt.Errorf("immutable result path collision for %s", id)
		}
	} else {
		written, err := f.Write(canonical)
		if err == nil && written != len(canonical) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_ = f.Close()
			_ = os.Remove(abs)
			return ResultRef{}, nil, err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(abs)
			return ResultRef{}, nil, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(abs)
			return ResultRef{}, nil, err
		}
	}
	ref := ResultRef{ID: id, ResultType: resultType, RelPath: relPath, SHA256: digest, SizeBytes: int64(len(canonical))}
	inline := append(json.RawMessage(nil), canonical[:len(canonical)-1]...)
	return ref, inline, nil
}

func ValidateResultRef(sessionDir string, ref ResultRef, expectedType string) (ResultRef, error) {
	ref.ID = strings.TrimSpace(ref.ID)
	ref.ResultType = strings.TrimSpace(ref.ResultType)
	ref.RelPath = filepath.ToSlash(strings.TrimSpace(ref.RelPath))
	ref.SHA256 = strings.ToLower(strings.TrimSpace(ref.SHA256))
	if ref.ID == "" || ref.ResultType == "" || ref.RelPath == "" || ref.SHA256 == "" || ref.SizeBytes <= 0 {
		return ResultRef{}, fmt.Errorf("result_ref requires id, result_type, rel_path, sha256, and positive size_bytes")
	}
	if expectedType = strings.TrimSpace(expectedType); expectedType != "" && ref.ResultType != expectedType {
		return ResultRef{}, fmt.Errorf("result_ref result_type %q does not match %q", ref.ResultType, expectedType)
	}
	if len(ref.SHA256) != sha256.Size*2 {
		return ResultRef{}, fmt.Errorf("result_ref sha256 must be a 64-character digest")
	}
	if _, err := hex.DecodeString(ref.SHA256); err != nil {
		return ResultRef{}, fmt.Errorf("result_ref sha256 must be hexadecimal")
	}
	if ref.ID != resultRefID(ref.ResultType, ref.SHA256) {
		return ResultRef{}, fmt.Errorf("result_ref id does not match result_type and sha256")
	}
	expectedRelPath := filepath.ToSlash(filepath.Join("artifacts", "results", ref.ID+".json"))
	if ref.RelPath != expectedRelPath {
		return ResultRef{}, fmt.Errorf("result_ref rel_path does not match id")
	}
	validated, err := ValidateArtifactRefs(sessionDir, []ArtifactRef{{RelPath: ref.RelPath, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}})
	if err != nil {
		return ResultRef{}, err
	}
	if len(validated) != 1 {
		return ResultRef{}, fmt.Errorf("result_ref validation failed")
	}
	return ref, nil
}
