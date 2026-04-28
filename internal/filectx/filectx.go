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
	opts = opts.defaults()
	if len(paths) == 0 || resolvePath == nil {
		return BuildFilePartsResult{}
	}
	result := BuildFilePartsResult{
		Parts: make([]message.ContentPart, 0, len(paths)),
	}
	remainingTotal := opts.MaxTotalBytes
	for i, path := range paths {
		if opts.MaxTotalBytes > 0 && remainingTotal <= 0 {
			result.OmittedFiles += len(paths) - i
			break
		}
		if opts.MaxTotalBytes > 0 && result.LoadedFiles > 0 && remainingTotal < opts.MaxFileBytes {
			result.OmittedFiles += len(paths) - i
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

		effectiveMaxBytes := opts.MaxFileBytes
		if opts.MaxTotalBytes > 0 && remainingTotal < effectiveMaxBytes {
			effectiveMaxBytes = remainingTotal
		}
		if effectiveMaxBytes <= 0 {
			result.OmittedFiles += len(paths) - i
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
		result.Parts = append(result.Parts, message.ContentPart{
			Type: "text",
			Text: fmt.Sprintf("<file path=%q>\n%s\n</file>", path, body),
		})
		result.LoadedFiles++
		result.TotalBytes += len(data)
		if opts.MaxTotalBytes > 0 {
			remainingTotal -= len(data)
		}
	}
	if opts.MaxTotalBytes > 0 && result.OmittedFiles > 0 {
		result.Parts = append(result.Parts, message.ContentPart{
			Type: "text",
			Text: fmt.Sprintf("[... additional files omitted after reaching the total file-context budget (%s)]", formatByteBudget(opts.MaxTotalBytes)),
		})
	}
	return result
}
