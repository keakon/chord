package message

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"unicode"
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

func (c SearchCitation) markdownLink() string {
	label := strings.TrimSpace(c.Title)
	if label == "" {
		label = c.URL
	}
	label = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "\n", " ", "\r", " ").Replace(label)
	destination := strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20").Replace(c.URL)
	return "[" + label + "](<" + destination + ">)"
}

// CitedContent projects visible, clickable citations without modifying the raw
// blocks needed for provider replay. Missing offsets place links at block end.
func (h *NativeToolHistory) CitedContent(content string) string {
	if h == nil {
		return content
	}
	blocks := searchTextBlocks(h.Items)
	var plain, cited strings.Builder
	for _, block := range blocks {
		plain.WriteString(block.text)
		runes := []rune(block.text)
		citations := slices.Clone(block.citations)
		position := func(c SearchCitation) int {
			if c.validRange(block.text) {
				return *c.end
			}
			end := len(runes)
			for end > 0 && unicode.IsSpace(runes[end-1]) {
				end--
			}
			return end
		}
		slices.SortStableFunc(citations, func(a, b SearchCitation) int { return position(a) - position(b) })
		cursor := 0
		seen := make(map[struct {
			url string
			end int
		}]bool)
		for _, c := range citations {
			end := position(c)
			textEnd := end
			if c.validRange(block.text) && *c.start >= cursor && strings.HasPrefix(string(runes[*c.start:end]), "cite") {
				// Responses citation markers are protocol tokens, not prose.
				textEnd = *c.start
			}
			cited.WriteString(string(runes[cursor:textEnd]))
			cursor = end
			// Multiple citations at one position can share a source.
			key := struct {
				url string
				end int
			}{c.URL, end}
			if !seen[key] {
				cited.WriteByte(' ')
				cited.WriteString(c.markdownLink())
				seen[key] = true
			}
		}
		cited.WriteString(string(runes[cursor:]))
	}
	if plain.String() == content {
		return cited.String()
	}
	// Partial or nonstandard provider output must retain the terminal text.
	// Links still remain visible even when block-local offsets cannot be used.
	var fallback strings.Builder
	fallback.WriteString(content)
	seen := make(map[string]bool)
	for _, block := range blocks {
		for _, c := range block.citations {
			if !seen[c.URL] {
				fallback.WriteByte(' ')
				fallback.WriteString(c.markdownLink())
				seen[c.URL] = true
			}
		}
	}
	return fallback.String()
}
