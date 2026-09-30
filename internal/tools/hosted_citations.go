package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// hostedSearchCitation connects provider-authored citations to numbered URLs.
// Plain links in a model summary are deliberately not treated as sources.
type hostedSearchCitation struct{ URL, Title, Text string }

func webSearchObservationCitations(obs *message.HostedObservation) []hostedSearchCitation {
	var out []hostedSearchCitation
	for _, raw := range obs.Items {
		var item struct {
			Type      string            `json:"type"`
			Text      string            `json:"text"`
			Citations []json.RawMessage `json:"citations"`
			Content   []struct {
				Type        string            `json:"type"`
				Text        string            `json:"text"`
				Annotations []json.RawMessage `json:"annotations"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		add := func(raw json.RawMessage, text string) {
			var c struct {
				Type      string `json:"type"`
				URL       string `json:"url"`
				Title     string `json:"title"`
				CitedText string `json:"cited_text"`
				Start     *int   `json:"start_index"`
				End       *int   `json:"end_index"`
			}
			if json.Unmarshal(raw, &c) != nil || c.URL == "" || (c.Type != "url_citation" && c.Type != "web_search_result_location") {
				return
			}
			fragment := c.CitedText
			if fragment == "" && c.Start != nil && c.End != nil {
				runes := []rune(text)
				if *c.Start >= 0 && *c.End >= *c.Start && *c.End <= len(runes) {
					fragment = string(runes[*c.Start:*c.End])
				}
			}
			out = append(out, hostedSearchCitation{c.URL, c.Title, fragment})
		}
		for _, c := range item.Citations {
			add(c, item.Text)
		}
		for _, part := range item.Content {
			for _, c := range part.Annotations {
				add(c, part.Text)
			}
		}
	}
	return out
}

func formatWebSearchCitationMap(obs *message.HostedObservation, sources []hostedSearchSource) string {
	numbers := make(map[string]int, len(sources))
	for i, source := range sources {
		numbers[source.URL] = i + 1
	}
	var b strings.Builder
	seen := make(map[string]bool)
	for _, c := range webSearchObservationCitations(obs) {
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
