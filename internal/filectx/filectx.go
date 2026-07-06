package filectx

import (
	"bytes"
	"fmt"
	"os"

	"github.com/keakon/chord/internal/message"
)

const MaxFileBytes = 40 * 1024

type BuildFilePartsOptions struct {
	// MaxFileBytes limits the number of bytes kept per file. Defaults to MaxFileBytes.
	MaxFileBytes int
	// MaxTotalBytes limits the total bytes kept across all loaded files. Zero means unlimited.
	MaxTotalBytes int
}

type LineRange struct {
	Start int
	End   int
}

func (r LineRange) IsSet() bool {
	return r.Start > 0 || r.End > 0
}

func (r LineRange) IsValid() bool {
	return r.Start > 0 && r.End >= r.Start
}

func (r LineRange) String() string {
	if !r.IsValid() {
		return ""
	}
	if r.Start == r.End {
		return fmt.Sprintf("%d", r.Start)
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

type FileRef struct {
	Path  string
	Lines LineRange
}

type BuildFilePartsResult struct {
	Parts          []message.ContentPart
	LoadedFiles    int
	TruncatedFiles int
	OmittedFiles   int
	TotalBytes     int
}

func (o BuildFilePartsOptions) defaults() BuildFilePartsOptions {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = MaxFileBytes
	}
	if o.MaxTotalBytes < 0 {
		o.MaxTotalBytes = 0
	}
	return o
}

func formatByteBudget(limit int) string {
	if limit <= 0 {
		return "0 bytes"
	}
	if limit%1024 == 0 {
		return fmt.Sprintf("%d KB", limit/1024)
	}
	return fmt.Sprintf("%d bytes", limit)
}

// BuildFileParts reads the given display paths via resolvePath and encodes each
// successfully loaded file as a <file path="...">...</file> text part.
//
// Paths are preserved exactly as provided in the wrapper so the model sees the
// same display path the caller chose to reference.
func BuildFileParts(paths []string, resolvePath func(string) string) []message.ContentPart {
	return BuildFilePartsWithOptions(paths, resolvePath, BuildFilePartsOptions{}).Parts
}

// BuildFilePartsWithOptions reads the given display paths via resolvePath and
// encodes each successfully loaded file as a <file path="...">...</file> text
// part, honoring per-file and optional total byte budgets.
func BuildFilePartsWithOptions(paths []string, resolvePath func(string) string, opts BuildFilePartsOptions) BuildFilePartsResult {
	refs := make([]FileRef, 0, len(paths))
	for _, path := range paths {
		refs = append(refs, FileRef{Path: path})
	}
	return BuildFileRefPartsWithOptions(refs, resolvePath, opts)
}

// BuildFileRefParts reads the given display file references via resolvePath and
// encodes each successfully loaded file as a <file path="...">...</file> text
// part. References with Lines set include only that 1-based inclusive line range
// and add a lines="..." attribute while keeping path as the actual file path.
func BuildFileRefParts(refs []FileRef, resolvePath func(string) string) []message.ContentPart {
	return BuildFileRefPartsWithOptions(refs, resolvePath, BuildFilePartsOptions{}).Parts
}

// BuildFileRefPartsWithOptions is BuildFileRefParts with per-file and optional
// total byte budgets.
func BuildFileRefPartsWithOptions(refs []FileRef, resolvePath func(string) string, opts BuildFilePartsOptions) BuildFilePartsResult {
	opts = opts.defaults()
	if len(refs) == 0 || resolvePath == nil {
		return BuildFilePartsResult{}
	}
	result := BuildFilePartsResult{
		Parts: make([]message.ContentPart, 0, len(refs)),
	}
	remainingTotal := opts.MaxTotalBytes
	for i, ref := range refs {
		path := ref.Path
		if opts.MaxTotalBytes > 0 && remainingTotal <= 0 {
			result.OmittedFiles += len(refs) - i
			break
		}
		if opts.MaxTotalBytes > 0 && result.LoadedFiles > 0 && remainingTotal < opts.MaxFileBytes {
			result.OmittedFiles += len(refs) - i
			break
		}
		resolved := resolvePath(path)
		if resolved == "" {
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			continue
		}
		if ref.Lines.IsSet() {
			if !ref.Lines.IsValid() {
				continue
			}
			selected, ok := selectLineRange(data, ref.Lines)
			if !ok {
				continue
			}
			data = selected
		}

		effectiveMaxBytes := opts.MaxFileBytes
		if opts.MaxTotalBytes > 0 && remainingTotal < effectiveMaxBytes {
			effectiveMaxBytes = remainingTotal
		}
		if effectiveMaxBytes <= 0 {
			result.OmittedFiles += len(refs) - i
			break
		}

		truncated := false
		if len(data) > effectiveMaxBytes {
			data = data[:effectiveMaxBytes]
			if idx := bytes.LastIndexByte(data, '\n'); idx > 0 {
				data = data[:idx]
			}
			truncated = true
		}
		body := string(data)
		if truncated {
			body += fmt.Sprintf("\n[...truncated, showing first %s only]", formatByteBudget(effectiveMaxBytes))
			result.TruncatedFiles++
		}
		openTag := fmt.Sprintf("%s%q", message.FileRefOpenTag, path)
		if ref.Lines.IsSet() {
			openTag += fmt.Sprintf(" lines=%q", ref.Lines.String())
		}
		result.Parts = append(result.Parts, message.ContentPart{
			Type: message.ContentPartText,
			Text: fmt.Sprintf("%s>\n%s\n</file>", openTag, body),
		})
		result.LoadedFiles++
		result.TotalBytes += len(data)
		if opts.MaxTotalBytes > 0 {
			remainingTotal -= len(data)
		}
	}
	if opts.MaxTotalBytes > 0 && result.OmittedFiles > 0 {
		result.Parts = append(result.Parts, message.ContentPart{
			Type: message.ContentPartText,
			Text: fmt.Sprintf("[... additional files omitted after reaching the total file-context budget (%s)]", formatByteBudget(opts.MaxTotalBytes)),
		})
	}
	return result
}

func selectLineRange(data []byte, lineRange LineRange) ([]byte, bool) {
	if !lineRange.IsValid() || len(data) == 0 {
		return nil, false
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if lineRange.Start > len(lines) {
		return nil, false
	}
	end := lineRange.End
	if end > len(lines) {
		end = len(lines)
	}
	return bytes.Join(lines[lineRange.Start-1:end], nil), true
}
