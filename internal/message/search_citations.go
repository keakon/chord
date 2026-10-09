package message

import (
	"encoding/json"
	"net/url"
	"strings"
)

// SearchCitation is a provider-authored web citation, not a link inferred from
// generated prose. Offsets are local to the containing text block.
type SearchCitation struct {
	URL, Title, Text string
	start, end       *int
}

type searchTextBlock struct {
	text      string
	citations []SearchCitation
}

func searchTextBlocks(items []json.RawMessage) []searchTextBlock {
	var blocks []searchTextBlock
	add := func(text string, rawCitations []json.RawMessage) {
		block := searchTextBlock{text: text}
		for _, raw := range rawCitations {
			var c struct {
				Type      string `json:"type"`
				URL       string `json:"url"`
				Title     string `json:"title"`
				CitedText string `json:"cited_text"`
				Start     *int   `json:"start_index"`
				End       *int   `json:"end_index"`
			}
			if json.Unmarshal(raw, &c) != nil || (c.Type != "url_citation" && c.Type != "web_search_result_location") {
				continue
			}
			u, err := url.Parse(strings.TrimSpace(c.URL))
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				continue
			}
			citation := SearchCitation{URL: u.String(), Title: c.Title, Text: c.CitedText, start: c.Start, end: c.End}
			if citation.Text == "" && citation.validRange(text) {
				citation.Text = string([]rune(text)[*c.Start:*c.End])
			}
			block.citations = append(block.citations, citation)
		}
		blocks = append(blocks, block)
	}
	for _, raw := range items {
		var item struct {
			Type      string            `json:"type"`
			Text      string            `json:"text"`
			Citations []json.RawMessage `json:"citations"`
			Content   json.RawMessage   `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch item.Type {
		case "text":
			add(item.Text, item.Citations)
		case "message":
			var parts []struct {
				Type        string            `json:"type"`
				Text        string            `json:"text"`
				Annotations []json.RawMessage `json:"annotations"`
			}
			if json.Unmarshal(item.Content, &parts) == nil {
				for _, part := range parts {
					if part.Type == "output_text" {
						add(part.Text, part.Annotations)
					}
				}
			}
		}
	}
	return blocks
}

func (c SearchCitation) validRange(text string) bool {
	return c.start != nil && c.end != nil && *c.start >= 0 && *c.end >= *c.start && *c.end <= len([]rune(text))
}

// WebSearchCitations shares source extraction between hosted and native views.
func WebSearchCitations(items []json.RawMessage) []SearchCitation {
	var out []SearchCitation
	for _, block := range searchTextBlocks(items) {
		out = append(out, block.citations...)
	}
	return out
}
