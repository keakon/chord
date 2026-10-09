package tools

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

func formatWebSearchCitationMap(obs *message.HostedObservation, sources []hostedSearchSource) string {
	numbers := make(map[string]int, len(sources))
	for i, source := range sources {
		numbers[source.URL] = i + 1
	}
	var b strings.Builder
	seen := make(map[string]bool)
	for _, c := range message.WebSearchCitations(obs.Items) {
		text := strings.TrimSpace(c.Text)
		number := numbers[strings.TrimSpace(c.URL)]
		if text == "" || number == 0 {
			continue
		}
		key := fmt.Sprintf("%d:%s", number, text)
		if seen[key] {
			continue
		}
		seen[key] = true
		if b.Len() == 0 {
			b.WriteString("Cited passages:\n")
		}
		fmt.Fprintf(&b, "[%d] %s\n", number, text)
	}
	return b.String()
}
