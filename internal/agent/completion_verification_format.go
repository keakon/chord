package agent

import "strings"

func formatCompletionVerificationRecords(records []VerificationRecord) string {
	items := make([]string, 0, len(records))
	for _, record := range records {
		item := record.Command + " [" + record.Status + "]"
		if record.Summary != "" {
			item += ": " + record.Summary
		}
		items = append(items, item)
	}
	return strings.Join(items, "; ")
}
