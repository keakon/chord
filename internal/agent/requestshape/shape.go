// Package requestshape fingerprints exact request surfaces for safe reuse.
package requestshape

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"maps"
	"slices"

	"github.com/keakon/chord/internal/message"
)

// Shape is an immutable, comparable fingerprint used to validate reusable
// request surfaces. It includes local metadata that can affect projection.
type Shape struct {
	deliveryHash          [32]byte
	role                  message.Role
	contentHash           [sha256.Size]byte
	partsHash             [sha256.Size]byte
	thinkingHash          [sha256.Size]byte
	responsesOutputHash   [sha256.Size]byte
	geminiPartsHash       [sha256.Size]byte
	reasoningHash         [sha256.Size]byte
	compactionFilesHash   [sha256.Size]byte
	compactionSourcesHash [sha256.Size]byte
	toolCallsHash         [sha256.Size]byte
	toolCallID            string
	requestBatch          uint64
	toolDiffHash          [sha256.Size]byte
	toolDiffAdded         int
	toolDiffRemoved       int
	toolStatus            string
	provenance            provenanceShape
	isCompactionSummary   bool
	kind                  string
	toolRecoveryState     string
}

type provenanceShape struct {
	source     string
	providerID string
	modelID    string
	variant    string
	modelRef   string
	wireFamily string
	imported   bool
}

// ShapesCompatible reports whether messages still match recorded shapes.
func ShapesCompatible(previous []Shape, current []message.Message) bool {
	if len(previous) != len(current) {
		return false
	}
	currentShape := Shapes(current)
	for i := range previous {
		if previous[i] != currentShape[i] {
			return false
		}
	}
	return true
}

// Shapes fingerprints every message in order.
func Shapes(messages []message.Message) []Shape {
	if len(messages) == 0 {
		return nil
	}
	shapes := make([]Shape, len(messages))
	for i := range messages {
		shapes[i] = ShapeOf(&messages[i])
	}
	return shapes
}

// ShapeOf fingerprints the fields that affect request-surface reuse.
func ShapeOf(msg *message.Message) Shape {
	return Shape{
		role:                  msg.Role,
		deliveryHash:          deliveryMetadataHash(*msg),
		contentHash:           contentHash(msg.Content),
		partsHash:             contentPartsHash(msg.Parts),
		thinkingHash:          thinkingBlocksHash(msg.ThinkingBlocks),
		responsesOutputHash:   responsesOutputHash(msg.ResponsesOutput),
		geminiPartsHash:       geminiPartsHash(msg.GeminiParts),
		reasoningHash:         contentHash(msg.ReasoningContent),
		compactionFilesHash:   stringMapHash(msg.CompactionFileRevisions),
		compactionSourcesHash: stringMapHash(msg.CompactionRequestSources),
		toolCallsHash:         toolCallsHash(msg.ToolCalls),
		toolCallID:            msg.ToolCallID,
		requestBatch:          msg.RequestBatch,
		toolDiffHash:          contentHash(msg.ToolDiff),
		toolDiffAdded:         msg.ToolDiffAdded,
		toolDiffRemoved:       msg.ToolDiffRemoved,
		toolStatus:            msg.ToolStatus,
		provenance:            provenanceShapeFor(msg.Provenance),
		isCompactionSummary:   msg.IsCompactionSummary,
		kind:                  msg.Kind,
		toolRecoveryState:     msg.ToolRecoveryState,
	}
}

// MessagesEquivalent reports whether every message pair would
// produce identical Shape values. It compares exactly
// the fields the shape hashes cover, using direct equality instead of
// hashing: string comparison short-circuits on shared backing arrays, so an
// unchanged (append-only) history costs O(1) per message with no allocation.
// Field equality implies hash equality, so this is strictly at least as
// precise as comparing hashes.
func MessagesEquivalent(source, current []message.Message) bool {
	return len(source) == len(current) && ReusablePrefixLen(source, current) == len(source)
}

// MessageEquivalent compares the fields covered by ShapeOf without allocating hashes for unchanged text.
func MessageEquivalent(a, b *message.Message) bool {
	if a.Role != b.Role ||
		a.Content != b.Content ||
		a.ReasoningContent != b.ReasoningContent ||
		a.ToolCallID != b.ToolCallID ||
		a.RequestBatch != b.RequestBatch ||
		a.ToolDiff != b.ToolDiff ||
		a.ToolDiffAdded != b.ToolDiffAdded ||
		a.ToolDiffRemoved != b.ToolDiffRemoved ||
		a.ToolStatus != b.ToolStatus ||
		a.IsCompactionSummary != b.IsCompactionSummary ||
		a.Kind != b.Kind ||
		a.ToolRecoveryState != b.ToolRecoveryState {
		return false
	}
	if deliveryMetadataHash(*a) != deliveryMetadataHash(*b) {
		return false
	}
	if !maps.Equal(a.CompactionRequestSources, b.CompactionRequestSources) || !maps.Equal(a.CompactionFileRevisions, b.CompactionFileRevisions) {
		return false
	}
	if provenanceShapeFor(a.Provenance) != provenanceShapeFor(b.Provenance) {
		return false
	}
	if len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		ac, bc := &a.ToolCalls[i], &b.ToolCalls[i]
		if ac.ID != bc.ID || ac.Name != bc.Name || ac.ThoughtSignature != bc.ThoughtSignature || !bytes.Equal(ac.Args, bc.Args) {
			return false
		}
	}
	if len(a.ThinkingBlocks) != len(b.ThinkingBlocks) {
		return false
	}
	for i := range a.ThinkingBlocks {
		if a.ThinkingBlocks[i] != b.ThinkingBlocks[i] {
			return false
		}
	}
	if !slices.Equal(a.GeminiParts, b.GeminiParts) || !responsesOutputItemsEqual(a.ResponsesOutput, b.ResponsesOutput) {
		return false
	}
	if len(a.Parts) != len(b.Parts) {
		return false
	}
	for i := range a.Parts {
		ap, bp := &a.Parts[i], &b.Parts[i]
		if ap.Type != bp.Type ||
			ap.Text != bp.Text ||
			ap.DisplayText != bp.DisplayText ||
			ap.InlineToken != bp.InlineToken ||
			ap.MimeType != bp.MimeType ||
			ap.ImagePath != bp.ImagePath ||
			ap.FileName != bp.FileName ||
			!bytes.Equal(ap.Data, bp.Data) {
			return false
		}
		// The shape hash folds in DataBytes for a part whose payload is not
		// inlined, so the equality check has to as well or it would claim
		// equality the hash denies.
		if len(ap.Data) == 0 && ap.DataBytes != bp.DataBytes {
			return false
		}
	}
	return true
}

func provenanceShapeFor(provenance *message.MessageProvenance) provenanceShape {
	if provenance == nil {
		return provenanceShape{}
	}
	return provenanceShape{
		source:     provenance.Source,
		providerID: provenance.ProviderID,
		modelID:    provenance.ModelID,
		variant:    provenance.Variant,
		modelRef:   provenance.ModelRef,
		wireFamily: provenance.WireFamily,
		imported:   provenance.Imported,
	}
}

func stringMapHash(values map[string]string) [sha256.Size]byte {
	if len(values) == 0 {
		return emptySequenceHash
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	h := sha256.New()
	writeInt(h, len(keys))
	for _, key := range keys {
		writeString(h, key)
		writeString(h, values[key])
	}
	return hashBytes(h.Sum(nil))
}

var emptySequenceHash = func() [sha256.Size]byte {
	h := sha256.New()
	writeInt(h, 0)
	return hashBytes(h.Sum(nil))
}()

func contentPartsHash(parts []message.ContentPart) [sha256.Size]byte {
	if len(parts) == 0 {
		return emptySequenceHash
	}
	h := sha256.New()
	writeInt(h, len(parts))
	for _, part := range parts {
		writeString(h, string(part.Type))
		writeString(h, part.Text)
		writeString(h, part.DisplayText)
		writeString(h, part.InlineToken)
		writeString(h, part.MimeType)
		writeBytes(h, part.Data)
		// A restored part keeps Data empty and its size on DataBytes, so the
		// fingerprint distinguishes it from a part with no payload at all.
		if len(part.Data) == 0 {
			writeInt(h, int(part.DataBytes))
		}
		writeString(h, part.ImagePath)
		writeString(h, part.FileName)
	}
	return hashBytes(h.Sum(nil))
}

func thinkingBlocksHash(blocks []message.ThinkingBlock) [sha256.Size]byte {
	if len(blocks) == 0 {
		return emptySequenceHash
	}
	h := sha256.New()
	writeInt(h, len(blocks))
	for _, block := range blocks {
		writeString(h, block.Thinking)
		writeString(h, block.Signature)
		writeString(h, block.Data)
	}
	return hashBytes(h.Sum(nil))
}

func toolCallsHash(calls []message.ToolCall) [sha256.Size]byte {
	if len(calls) == 0 {
		return emptySequenceHash
	}
	h := sha256.New()
	writeInt(h, len(calls))
	for _, call := range calls {
		writeString(h, call.ID)
		writeString(h, call.Name)
		writeBytes(h, call.Args)
		writeString(h, call.ThoughtSignature)
	}
	return hashBytes(h.Sum(nil))
}

func responsesOutputHash(items []message.ResponsesOutputItem) [sha256.Size]byte {
	if len(items) == 0 {
		return emptySequenceHash
	}
	h := sha256.New()
	writeInt(h, len(items))
	for _, item := range items {
		writeString(h, item.Type)
		writeString(h, item.ID)
		writeString(h, item.CallID)
		writeString(h, item.Role)
		writeString(h, item.Name)
		writeString(h, item.Arguments)
		writeString(h, item.Phase)
		writeString(h, item.EncryptedContent)
		writeInt(h, len(item.Content))
		for _, content := range item.Content {
			writeString(h, content.Type)
			writeString(h, content.Text)
			writeString(h, content.Refusal)
		}
		writeInt(h, len(item.Summary))
		for _, summary := range item.Summary {
			writeString(h, summary.Type)
			writeString(h, summary.Text)
		}
	}
	return hashBytes(h.Sum(nil))
}

func geminiPartsHash(parts []message.GeminiReplayPart) [sha256.Size]byte {
	if len(parts) == 0 {
		return emptySequenceHash
	}
	h := sha256.New()
	writeInt(h, len(parts))
	for _, part := range parts {
		writeString(h, part.Type)
		writeString(h, part.Text)
		writeString(h, part.ToolCallID)
		writeString(h, part.ThoughtSignature)
	}
	return hashBytes(h.Sum(nil))
}

func responsesOutputItemsEqual(a, b []message.ResponsesOutputItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].ID != b[i].ID || a[i].CallID != b[i].CallID || a[i].Role != b[i].Role || a[i].Name != b[i].Name || a[i].Arguments != b[i].Arguments || a[i].Phase != b[i].Phase || a[i].EncryptedContent != b[i].EncryptedContent || !slices.Equal(a[i].Content, b[i].Content) || !slices.Equal(a[i].Summary, b[i].Summary) {
			return false
		}
	}
	return true
}

func hashBytes(value []byte) [sha256.Size]byte {
	return sha256.Sum256(value)
}

func writeString(h interface{ Write([]byte) (int, error) }, value string) {
	writeBytes(h, []byte(value))
}

func writeBytes(h interface{ Write([]byte) (int, error) }, value []byte) {
	writeInt(h, len(value))
	_, _ = h.Write(value)
}

func writeInt(h interface{ Write([]byte) (int, error) }, value int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(value))
	_, _ = h.Write(buf[:])
}

// ToolDefinitionsHash returns a stable hash of a tool surface. Name,
// description, and input schema all contribute because any of them changes the
// cacheable tool surface even when the others are unchanged. encoding/json
// marshals map keys in sorted order, so equal schemas hash to the same bytes
// across turns.
func ToolDefinitionsHash(defs []message.ToolDefinition) [sha256.Size]byte {
	h := sha256.New()
	writeInt(h, len(defs))
	for _, def := range defs {
		writeString(h, def.Name)
		writeString(h, def.Description)
		schema, err := json.Marshal(def.InputSchema)
		if err != nil {
			schema = nil
		}
		writeBytes(h, schema)
	}
	return hashBytes(h.Sum(nil))
}

func deliveryMetadataHash(m message.Message) [32]byte {
	if len(m.DeliveredFiles) == 0 && len(m.CompactionDeliveredFiles) == 0 && m.FileLinkBaseDir == "" {
		return [32]byte{}
	}
	raw, _ := json.Marshal(struct {
		Files, Checkpoint []message.DeliveredFile
		Base              string
	}{m.DeliveredFiles, m.CompactionDeliveredFiles, m.FileLinkBaseDir})
	return contentHash(string(raw))
}

// ReusablePrefixLen finds the unchanged leading run of messages.
func ReusablePrefixLen(previous, current []message.Message) int {
	limit := min(len(previous), len(current))
	reusable := 0
	for reusable < limit && MessageEquivalent(&previous[reusable], &current[reusable]) {
		reusable++
	}
	return reusable
}

func contentHash(value string) [sha256.Size]byte {
	h := sha256.New()
	_, _ = io.WriteString(h, value)
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}
