package tui

func (m *Model) filterAtMentionOptionsByInputSupport(options []atMentionOption) []atMentionOption {
	if len(options) == 0 {
		return options
	}
	out := options[:0]
	for _, option := range options {
		if option.IsDir {
			out = append(out, option)
			continue
		}
		kind := attachmentKindForPath(unescapeAtMentionPath(option.Path))
		if kind != "" && !m.supportsAttachmentInput(kind) {
			continue
		}
		out = append(out, option)
	}
	return out
}
