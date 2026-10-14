package tui

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/filecompletion"
)

// buildFileRefParts scans displayText and extraTexts for @-mentions, reads each
// referenced file into <file path=...> parts, and places them after composerParts
// so the model sees the user's prompt first, then file contents. Each composer
// text part keeps its full Text (raw pasted content) and optional DisplayText
// (inline placeholder for the composer UI), so multiple large pastes stay in the
// correct positions in the transcript and in the payload to the model.
func (m *Model) buildFileRefParts(displayText string, composerParts []message.ContentPart, extraTexts ...string) []message.ContentPart {
	texts := make([]string, 0, 1+len(extraTexts))
	if displayText != "" {
		texts = append(texts, displayText)
	}
	for _, extra := range extraTexts {
		if extra != "" {
			texts = append(texts, extra)
		}
	}
	refs := filecompletion.DedupeRefs(filecompletion.ParseRefs(texts, m.workingDir), m.workingDir)
	if len(refs) == 0 {
		return nil
	}
	textRefs, attachmentParts := m.splitAtMentionRefs(refs)
	fileParts := filectx.BuildFileRefParts(textRefs, m.resolveFileRefPath)
	if len(fileParts) == 0 && len(attachmentParts) == 0 {
		return nil
	}
	parts := make([]message.ContentPart, 0, len(composerParts)+len(fileParts)+len(attachmentParts))
	for _, p := range composerParts {
		if p.Type != message.ContentPartText || message.IsFileRefContent(p.Text) {
			continue
		}
		parts = append(parts, p)
	}
	parts = append(parts, attachmentParts...)
	parts = append(parts, fileParts...)
	return parts
}

func (m *Model) splitAtMentionRefs(refs []filecompletion.Reference) ([]filectx.FileRef, []message.ContentPart) {
	if len(refs) == 0 {
		return nil, nil
	}
	textRefs := make([]filectx.FileRef, 0, len(refs))
	attachments := make([]message.ContentPart, 0, len(refs))
	for _, ref := range refs {
		resolved := m.resolveFileRefPath(ref.Path)
		kind := filecompletion.AttachmentKindForPath(resolved)
		if kind == "" || ref.Lines.IsSet() {
			textRefs = append(textRefs, filectx.FileRef{Path: ref.Path, Lines: ref.Lines})
			continue
		}
		data, mimeType, err := imageutil.ReadAttachmentFile(context.Background(), resolved)
		if err != nil {
			textRefs = append(textRefs, filectx.FileRef{Path: ref.Path, Lines: ref.Lines})
			continue
		}
		attachments = append(attachments, message.ContentPart{
			Type:      kind,
			MimeType:  mimeType,
			Data:      data,
			ImagePath: resolved,
			FileName:  filepath.Base(resolved),
		})
	}
	return textRefs, attachments
}

func (m *Model) resolveFileRefPath(path string) string {
	return filecompletion.ResolvePath(path, m.workingDir)
}

func inputLineAt(value string, line int) (string, bool) {
	lines := strings.Split(value, "\n")
	if line < 0 || line >= len(lines) {
		return "", false
	}
	return lines[line], true
}

func inputTokenAt(value string, line, startCol, endCol int) (string, bool) {
	row, ok := inputLineAt(value, line)
	if !ok {
		return "", false
	}
	runes := []rune(row)
	if startCol < 0 || startCol > len(runes) {
		return "", false
	}
	if endCol > len(runes) {
		endCol = len(runes)
	}
	segment := string(runes[startCol:endCol])
	escaped := false
	for _, r := range segment {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == ' ' || r == '\t':
			return "", false
		}
	}
	return segment, true
}

func canTriggerAtMention(row string, col int) bool {
	if col == 0 {
		return true
	}
	runes := []rune(row)
	if col < 0 || col > len(runes) {
		return false
	}
	return runes[col-1] == ' '
}
