package tools

// SelectedOptions resolves choices by their stable IDs against the immutable
// question snapshot. Labels may repeat; selection order comes from the answer.
func (a QuestionAnswer) SelectedOptions(item QuestionItem) []QuestionOption {
	if len(a.SelectedIDs) == 0 {
		return nil
	}
	selected := make([]QuestionOption, 0, len(a.SelectedIDs))
	for i, id := range a.SelectedIDs {
		choice := QuestionOption{ID: id}
		if i < len(a.Selected) {
			choice.Label = a.Selected[i]
		}
		for _, option := range item.Options {
			if option.ID == id {
				choice = option
				break
			}
		}
		selected = append(selected, choice)
	}
	return selected
}
