package tools

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type applyPatchDocument struct {
	Operations []applyPatchOperation
}

type applyPatchOperation struct {
	Kind     MutationKind
	Path     string
	MovePath string
	Content  string
	Hunks    []applyPatchHunk
}

type applyPatchHunk struct {
	Header    string
	Lines     []applyPatchLine
	EndOfFile bool
}

type applyPatchLine struct {
	Kind byte
	Text string
}

// isApplyPatchMarker reports whether a raw patch line starts a protocol
// section such as `*** Update File:`. Only raw lines qualify: hunk and
// add-file content always carries a '+', '-', or ' ' prefix, so trimming
// before this check would misread genuine file lines like ` *** heading`
// as protocol markers and reject the patch.
func isApplyPatchMarker(line string) bool {
	return strings.HasPrefix(line, "*** ")
}

// skipApplyPatchSeparatorRun handles blank lines that models commonly insert
// between operations or hunks. A run of empty lines counts as a separator only
// when it runs all the way to the next protocol boundary (`*** ` marker, a new
// `@@` hunk header when allowHunk is set, or `*** End Patch`); it then reports
// true with the boundary index. Interior blank runs are content, not
// separators, so it reports false — with the end of the run, letting callers
// consume the whole run at once instead of re-scanning per line.
func skipApplyPatchSeparatorRun(lines []string, i int, allowHunk bool) (int, bool) {
	j := i
	for j < len(lines)-1 && lines[j] == "" {
		j++
	}
	if j == i {
		return i, false
	}
	if j == len(lines)-1 || isApplyPatchMarker(lines[j]) || (allowHunk && strings.HasPrefix(lines[j], applyPatchHunkMarker)) {
		return j, true
	}
	return j, false
}

// applyPatchCarriedHeader reports whether hunk is the empty shell a model
// leaves behind when it writes the two-line header spelling — `@@`, one
// section-context line, then the hunk's own `@@`. Chord starts a new hunk at
// every `@@`, so that shell holds exactly one context line and no change; the
// line is the section the model wanted to anchor on, and it is carried onto
// the next hunk's header instead.
//
// Only a single non-blank context line qualifies. A longer context-only block
// stays an error: with two or more lines there is no way to tell the anchor
// from context the model meant to keep, and guessing would mis-anchor the
// following hunk.
func applyPatchCarriedHeader(hunk applyPatchHunk) (bool, string) {
	if len(hunk.Lines) != 1 {
		return false, ""
	}
	line := hunk.Lines[0]
	if line.Kind != ' ' {
		return false, ""
	}
	text := strings.TrimSpace(line.Text)
	if text == "" {
		return false, ""
	}
	return true, text
}

// unifiedDiffHeaderRE matches the line-range prefix of a unified-diff hunk
// header, e.g. "-19,10 +19,8" inside "@@ -19,10 +19,8 @@ func foo()". Chord
// anchors on the header text itself, not on line numbers, so the range is noise
// it can never locate; group 1 captures any trailing section text worth keeping
// as the real anchor.
var unifiedDiffHeaderRE = regexp.MustCompile(`^-\d+(?:,\d+)? \+\d+(?:,\d+)? ` + applyPatchHunkMarker + `\s?(.*)$`)

// applyPatchNormalizeHeader strips the unified-diff line-range noise from an @@
// header so it neither wastes tokens nor masquerades as an unlocatable anchor. A
// header that is only the range (e.g. "-19,10 +19,8 @@") collapses to empty,
// letting the hunk body anchor instead; a header with a trailing section (e.g.
// "@@ -19,10 +19,8 @@ func foo()") keeps that section as the real anchor. Headers
// in Chord's own spelling ("@@ func greet():") never start with "-<digits> +<digits>",
// so they pass through untouched.
func applyPatchNormalizeHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := unifiedDiffHeaderRE.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1])
	}
	return raw
}

func ParseApplyPatch(text string) (applyPatchDocument, error) {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n")
	lines, err := normalizeApplyPatchEnvelope(text)
	if err != nil {
		return applyPatchDocument{}, err
	}

	var doc applyPatchDocument
	for i := 1; i < len(lines)-1; {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		var op applyPatchOperation
		switch {
		case strings.HasPrefix(line, ApplyPatchAddFileMarker):
			op.Kind = MutationAdd
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, ApplyPatchAddFileMarker))
			i++
			var content strings.Builder
			for i < len(lines)-1 && !isApplyPatchMarker(lines[i]) {
				if next, ok := skipApplyPatchSeparatorRun(lines, i, false); ok {
					i = next
					continue
				}
				if !strings.HasPrefix(lines[i], "+") {
					return applyPatchDocument{}, fmt.Errorf("invalid add-file line %d: each line must start with +", i+1)
				}
				content.WriteString(lines[i][1:])
				content.WriteByte('\n')
				i++
			}
			op.Content = content.String()
			if op.Content == "" {
				return applyPatchDocument{}, fmt.Errorf("invalid add-file operation for %s: content is required", op.Path)
			}
		case strings.HasPrefix(line, ApplyPatchDeleteFileMarker):
			op.Kind = MutationDelete
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, ApplyPatchDeleteFileMarker))
			i++
		case strings.HasPrefix(line, ApplyPatchUpdateFileMarker):
			op.Kind = MutationUpdate
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, ApplyPatchUpdateFileMarker))
			i++
			if i < len(lines)-1 && strings.HasPrefix(strings.TrimSpace(lines[i]), ApplyPatchMoveToMarker) {
				op.MovePath = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ApplyPatchMoveToMarker))
				i++
			}
			// carriedHeader collects a lone context line stranded between two
			// `@@` markers by the two-line header spelling some models emit
			// (see applyPatchCarriedHeader). It only ever survives until the
			// next `@@`, so it cannot leak across operations.
			carriedHeader := ""
			for i < len(lines)-1 && !isApplyPatchMarker(lines[i]) {
				if lines[i] == "" {
					if next, ok := skipApplyPatchSeparatorRun(lines, i, true); ok {
						i = next
						continue
					}
				}
				implicitFirstHunk := len(op.Hunks) == 0 && (lines[i] == "" || strings.ContainsRune(" +-", rune(lines[i][0])))
				if !strings.HasPrefix(lines[i], applyPatchHunkMarker) && !implicitFirstHunk {
					return applyPatchDocument{}, fmt.Errorf("invalid update hunk at line %d: expected @@", i+1)
				}
				var h applyPatchHunk
				hunkStartLine := i + 1
				if !implicitFirstHunk {
					h.Header = applyPatchNormalizeHeader(strings.TrimSpace(strings.TrimPrefix(lines[i], applyPatchHunkMarker)))
					if h.Header == "" {
						h.Header = carriedHeader
					}
					carriedHeader = ""
					i++
				}
				for i < len(lines)-1 && !strings.HasPrefix(lines[i], applyPatchHunkMarker) && !isApplyPatchMarker(lines[i]) {
					if lines[i] == "" {
						next, ok := skipApplyPatchSeparatorRun(lines, i, true)
						if ok {
							i = next
							continue
						}
						for i < next {
							h.Lines = append(h.Lines, applyPatchLine{Kind: ' ', Text: ""})
							i++
						}
						continue
					}
					kind := lines[i][0]
					if kind != ' ' && kind != '+' && kind != '-' {
						return applyPatchDocument{}, fmt.Errorf("invalid patch line %d: expected space, +, or - marker", i+1)
					}
					h.Lines = append(h.Lines, applyPatchLine{Kind: kind, Text: lines[i][1:]})
					i++
				}
				if i < len(lines)-1 && strings.TrimSpace(lines[i]) == "*** End of File" {
					h.EndOfFile = true
					i++
				}
				// A context-only hunk stranded directly before another `@@` is
				// the two-line header spelling, not a navigation placeholder:
				// carry its single line forward as the next hunk's header and
				// drop the shell instead of failing. The rejection below only
				// covers orphans at the end of an operation, where the model
				// really did emit context with nothing to change.
				if carried, text := applyPatchCarriedHeader(h); carried && i < len(lines)-1 && strings.HasPrefix(lines[i], applyPatchHunkMarker) {
					carriedHeader = text
					continue
				}
				if len(h.Lines) == 0 {
					return applyPatchDocument{}, fmt.Errorf("invalid empty update hunk for %s", op.Path)
				}
				if !slices.ContainsFunc(h.Lines, func(line applyPatchLine) bool {
					return line.Kind == '+' || line.Kind == '-'
				}) {
					return applyPatchDocument{}, fmt.Errorf("invalid update hunk for %s at line %d: at least one added or removed line is required; context-only or whitespace-only lines are not omission placeholders. To anchor a hunk, put its section context on the `@@` line itself (`@@ func foo():`) and rebuild the hunk from a fresh read of the target range", op.Path, hunkStartLine)
				}
				op.Hunks = append(op.Hunks, h)
			}
			if len(op.Hunks) == 0 && op.MovePath == "" {
				return applyPatchDocument{}, fmt.Errorf("invalid update operation for %s: at least one hunk is required", op.Path)
			}
		default:
			return applyPatchDocument{}, fmt.Errorf("invalid apply_patch operation at line %d: %s", i+1, line)
		}
		if strings.TrimSpace(op.Path) == "" {
			return applyPatchDocument{}, fmt.Errorf("apply_patch operation at line %d has an empty path", i+1)
		}
		doc.Operations = append(doc.Operations, op)
	}
	if len(doc.Operations) == 0 {
		return applyPatchDocument{}, fmt.Errorf("no files were modified")
	}
	return doc, nil
}

func normalizeApplyPatchEnvelope(text string) ([]string, error) {
	lines := strings.Split(text, "\n")
	strictBegin := strings.TrimSpace(lines[0]) == "*** Begin Patch"
	strictEnd := strings.TrimSpace(lines[len(lines)-1]) == "*** End Patch"
	if strictBegin && strictEnd {
		return lines, nil
	}

	normalized := append([]string(nil), lines...)
	if !strictBegin {
		first := strings.TrimSpace(normalized[0])
		switch {
		case strings.HasPrefix(first, "*** Begin Patch"):
			normalized[0] = "*** Begin Patch"
		case isApplyPatchTopLevelOperation(first):
			normalized = append([]string{"*** Begin Patch"}, normalized...)
		default:
			return nil, fmt.Errorf("invalid apply_patch: first line must be `*** Begin Patch`")
		}
	}
	if strings.TrimSpace(normalized[len(normalized)-1]) != "*** End Patch" {
		last := strings.TrimSpace(normalized[len(normalized)-1])
		switch {
		case strings.HasPrefix(last, "*** End Patch"):
			normalized[len(normalized)-1] = "*** End Patch"
		case isApplyPatchImplicitEOF(normalized[len(normalized)-1]):
			normalized = append(normalized, "*** End Patch")
		default:
			return nil, fmt.Errorf("invalid apply_patch: last line must be `*** End Patch`")
		}
	}
	return normalized, nil
}

func isApplyPatchTopLevelOperation(line string) bool {
	return strings.HasPrefix(line, ApplyPatchAddFileMarker) ||
		strings.HasPrefix(line, ApplyPatchDeleteFileMarker) ||
		strings.HasPrefix(line, ApplyPatchUpdateFileMarker)
}

func isApplyPatchImplicitEOF(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed == "*** End of File" {
		return true
	}
	if strings.HasPrefix(trimmed, ApplyPatchDeleteFileMarker) || strings.HasPrefix(trimmed, ApplyPatchMoveToMarker) {
		return true
	}
	if strings.HasPrefix(line, applyPatchHunkMarker) {
		return true
	}
	kind := line[0]
	return kind == ' ' || kind == '+' || kind == '-'
}
