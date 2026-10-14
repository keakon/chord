package tui

import "github.com/keakon/chord/internal/tui/filecompletion"

func (m *Model) filterAtMentionOptionsByInputSupport(options []filecompletion.Option) []filecompletion.Option {
	if len(options) == 0 {
		return options
	}
	out := options[:0]
	for _, option := range options {
		if option.IsDir {
			out = append(out, option)
			continue
		}
		kind := filecompletion.AttachmentKindForPath(filecompletion.UnescapePath(option.Path))
		if kind != "" && !m.supportsAttachmentInput(kind) {
			continue
		}
		out = append(out, option)
	}
	return out
}
