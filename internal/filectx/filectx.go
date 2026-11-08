package filectx

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"github.com/keakon/chord/internal/message"
)

const MaxFileBytes = 40 * 1024

type BuildFilePartsOptions struct {
	// MaxFileBytes limits the number of bytes kept per file. Defaults to MaxFileBytes.
	MaxFileBytes int
	// MaxTotalBytes limits the total bytes kept across all loaded files. Zero means unlimited.
	MaxTotalBytes int
	// ReadFile optionally supplies the file contents. When nil, paths are read
	// with os.ReadFile after resolvePath.
	ReadFile func(string) ([]byte, error)
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
	// TotalBytes counts retained raw file bodies only. Wrappers, attributes,
	// truncation and omission notes are not part of this budget.
	TotalBytes int
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
// For plain UTF-8 text the wrapper also carries covered_lines, total_lines and,
// when applicable, truncated and partial_last_line, all computed from the same
// complete read as the retained raw body.
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
		readFile := opts.ReadFile
		if readFile == nil {
			readFile = os.ReadFile
		}
		data, err := readFile(resolved)
		if err != nil {
			continue
		}
		effectiveMaxBytes := opts.MaxFileBytes
		if opts.MaxTotalBytes > 0 && remainingTotal < effectiveMaxBytes {
			effectiveMaxBytes = remainingTotal
		}
		if effectiveMaxBytes <= 0 {
			result.OmittedFiles += len(refs) - i
			break
		}

		content := prepareFilePart(data, ref.Lines, effectiveMaxBytes)
		if !content.ok {
			continue
		}
		body := string(content.body)
		if content.truncated {
			body += fmt.Sprintf("\n[...truncated to fit the %s file-context byte budget; the file body is incomplete]", formatByteBudget(effectiveMaxBytes))
			result.TruncatedFiles++
		}
		openTag := fmt.Sprintf("%s%q", message.FileRefOpenTag, path)
		if ref.Lines.IsSet() {
			openTag += fmt.Sprintf(" lines=%q", ref.Lines.String())
		}
		openTag += fileLineAttributes(content)
		result.Parts = append(result.Parts, message.ContentPart{
			Type: message.ContentPartText,
			Text: fmt.Sprintf("%s>\n%s\n</file>", openTag, body),
		})
		result.LoadedFiles++
		result.TotalBytes += len(content.body)
		if opts.MaxTotalBytes > 0 {
			remainingTotal -= len(content.body)
		}
	}
	if opts.MaxTotalBytes > 0 && result.OmittedFiles > 0 {
		result.Parts = append(result.Parts, message.ContentPart{
			Type: message.ContentPartText,
			Text: OmittedFilesNote(result.OmittedFiles, opts.MaxTotalBytes),
		})
	}
	return result
}

// OmittedFilesNote builds the shared tail-omission note. Callers that drop
// additional whole file parts from an assembled bundle must regenerate it with
// the new omitted count instead of truncating an annotated part.
func OmittedFilesNote(omitted, totalBudget int) string {
	noun := "files"
	if omitted == 1 {
		noun = "file"
	}
	return fmt.Sprintf("[... %d additional %s omitted after reaching the total file-context budget (%s)]", omitted, noun, formatByteBudget(totalBudget))
}

// fileLineAttributes reports the coverage facts of the retained raw body.
// lines keeps its original meaning: the range the caller requested.
func fileLineAttributes(content filePartContent) string {
	if !content.hasFacts {
		return ""
	}
	covered := "none"
	if content.coveredStart > 0 && content.coveredEnd >= content.coveredStart {
		covered = fmt.Sprintf("%d-%d", content.coveredStart, content.coveredEnd)
	}
	attrs := fmt.Sprintf(" covered_lines=%q total_lines=%q", covered, strconv.Itoa(content.totalLines))
	if content.truncated {
		attrs += ` truncated="true"`
	}
	if content.partialLastLine {
		attrs += ` partial_last_line="true"`
	}
	return attrs
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
	end := min(lineRange.End, len(lines))
	return bytes.Join(lines[lineRange.Start-1:end], nil), true
}
