package inputhistory

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

type Range struct{ Start, End int }
type Query struct {
	Text, ProjectKey      string
	AllProjects, AllKinds bool
}
type Hit struct {
	ID         string
	Rank       int
	Summary    string
	Highlights []Range
}
type SearchResult struct {
	Hits         []Hit
	TotalMatched int
}

type searchCache struct {
	mu              sync.Mutex
	texts           map[string]string
	bytes, maxBytes int64
}

func cachedFolded(ctx context.Context, s Snapshot, r Record) (string, error) {
	if s.cache == nil {
		return folded(ctx, r)
	}
	c := s.cache
	c.mu.Lock()
	text, ok := c.texts[r.ID]
	c.mu.Unlock()
	if ok {
		return text, nil
	}
	text, err := folded(ctx, r)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.texts == nil {
		c.texts = make(map[string]string)
	}
	if _, ok = c.texts[r.ID]; !ok && c.bytes+int64(len(text)) <= c.maxBytes {
		c.texts[r.ID] = text
		c.bytes += int64(len(text))
	}
	return text, nil
}
func folded(ctx context.Context, r Record) (string, error) {
	var b strings.Builder
	size := 0
	for _, p := range r.TextParts {
		size += len(p.Text)
	}
	b.Grow(size)
	for _, p := range r.TextParts {
		for start := 0; start < len(p.Text); {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			end := min(len(p.Text), start+16<<10)
			for end < len(p.Text) && !utf8.RuneStart(p.Text[end]) {
				end--
			}
			b.WriteString(strings.Map(unicode.ToLower, p.Text[start:end]))
			start = end
		}
	}
	return b.String(), nil
}
func Match(ctx context.Context, s Snapshot, q Query) (SearchResult, error) {
	var out SearchResult
	query := strings.Map(unicode.ToLower, strings.Join(strings.Fields(NormalizeQuery(q.Text)), " "))
	words := strings.Fields(query)
	if len(words) > 16 {
		words = words[:16]
	}

	fuzzyAllowed := utf8.RuneCountInString(query) <= 64
	fuzzyNeedle := []rune(strings.Join(words, ""))
	for _, entry := range slices.Backward(s.Entries) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		r := entry.Record
		if (!q.AllProjects && r.ProjectKey != q.ProjectKey) || (!q.AllKinds && r.Kind != Prompt) {
			continue
		}
		var body string
		if query != "" {
			var err error
			body, err = cachedFolded(ctx, s, r)
			if err != nil {
				return out, err
			}
		}
		rank := -1
		if containsHistoryText(ctx, body, query) {
			rank = 0
		} else {
			all := true
			for _, w := range words {
				if !containsHistoryText(ctx, body, w) {
					all = false
					break
				}
			}
			if all {
				rank = 1
			} else if fuzzyAllowed && (len(body) <= 8192 || historyRuneLimit(ctx, body, 8192)) {
				needle := fuzzyNeedle
				at := 0
				checked := 0
				for offset, c := range body {
					if offset >= checked {
						if err := ctx.Err(); err != nil {
							return out, err
						}
						checked = offset + searchScanBytes
					}
					if at < len(needle) && needle[at] == c {
						at++
						if at == len(needle) {
							break
						}
					}
				}
				if at == len(needle) {
					rank = 2
				}
			}
		}
		if rank < 0 {
			continue
		}
		out.TotalMatched++
		position := 0
		for position < len(out.Hits) && out.Hits[position].Rank <= rank {
			position++
		}
		if position < 100 {
			page, _ := Preview(ctx, r, PreviewRequest{Width: 160, Lines: 1})
			hit := Hit{ID: r.ID, Rank: rank, Summary: strings.ReplaceAll(page.Text, "\n", " "), Highlights: highlightRanges(page.Text, query)}
			if len(out.Hits) < 100 {
				out.Hits = append(out.Hits, Hit{})
			}
			copy(out.Hits[position+1:], out.Hits[position:len(out.Hits)-1])
			out.Hits[position] = hit
		}
	}
	return out, ctx.Err()
}

type PreviewRequest struct {
	Checkpoint   int
	Query        string
	Offset       int
	Direction    int
	Width, Lines int
}
type PreviewPage struct {
	Text                string
	Highlights          []Range
	Start, End          int
	HasBefore, HasAfter bool
}

// Preview visits only a bounded byte window. Offsets refer to the concatenated original parts.
func Preview(ctx context.Context, r Record, req PreviewRequest) (PreviewPage, error) {
	start := max(0, req.Offset)
	if req.Direction < 0 {
		target := start
		req.Direction = 0
		req.Offset = max(0, min(req.Checkpoint, target-1))
		// The caller retains sparse checkpoints for the selected record only.
		// Cap the fallback for callers without checkpoints as well.
		req.Offset = max(req.Offset, target-(32<<10))
		origin := req.Offset
		for {
			page, err := Preview(ctx, r, req)
			if err != nil {
				return page, err
			}
			if page.End >= target || !page.HasAfter || page.End <= req.Offset || page.End-origin >= 64<<10 {
				return page, nil
			}
			req.Offset = page.End
		}
	}
	page := PreviewPage{Start: start, HasBefore: start > 0}
	var b strings.Builder
	absolute, scanned, col, lines := 0, 0, 0, 0
	limit, width := max(1, req.Lines), max(1, req.Width)
	finish := func(end int, more bool) (PreviewPage, error) {
		page.End = end
		page.HasAfter = more
		page.Text = strings.TrimSuffix(b.String(), "\n")
		page.Highlights = highlightRanges(page.Text, req.Query)
		return page, nil
	}
	for _, part := range r.TextParts {
		if absolute+len(part.Text) <= start {
			absolute += len(part.Text)
			continue
		}
		offset := max(0, start-absolute)
		if scanned >= 64<<10 {
			return finish(absolute+offset, true)
		}
		for offset < len(part.Text) && !utf8.RuneStart(part.Text[offset]) {
			offset++
		}
		// Bound the segment before constructing the grapheme iterator, including pathological combining sequences.
		stop := min(len(part.Text), offset+(64<<10)-scanned)
		for stop < len(part.Text) && stop > offset && !utf8.RuneStart(part.Text[stop]) {
			stop--
		}
		iter := uniseg.NewGraphemes(part.Text[offset:stop])
		for iter.Next() {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			from, to := iter.Positions()
			if lines >= limit {
				return finish(absolute+offset+from, true)
			}
			cluster := iter.Str()
			text := cluster
			columns := iter.Width()
			if cluster == "\n" || cluster == "\r\n" {
				b.WriteByte('\n')
				lines++
				col = 0
			} else {
				if strings.ContainsFunc(cluster, unicode.IsControl) {
					var escaped strings.Builder
					for _, c := range cluster {
						if unicode.IsControl(c) {
							quoted := strconv.QuoteRune(c)
							escaped.WriteString(quoted[1 : len(quoted)-1])
						} else {
							escaped.WriteRune(c)
						}
					}
					text = escaped.String()
					columns = uniseg.StringWidth(text)
				}
				if col > 0 && col+columns > width {
					b.WriteByte('\n')
					lines++
					col = 0
					if lines >= limit {
						return finish(absolute+offset+from, true)
					}
				}
				b.WriteString(text)
				col += columns
			}
			scanned += to - from
		}
		if stop < len(part.Text) {
			return finish(absolute+stop, true)
		}
		absolute += len(part.Text)
	}
	return finish(absolute, false)
}

// highlightRanges is bounded by the displayed window and never scans a complete record.
func highlightRanges(text, query string) []Range {
	words := strings.Fields(strings.Map(unicode.ToLower, query))
	if len(words) > 16 {
		words = words[:16]
	}
	lower := strings.Map(unicode.ToLower, text)
	var ranges []Range
	for _, word := range words {
		if word == "" {
			continue
		}
		offset := 0
		for len(ranges) < 256 {
			pos := strings.Index(lower[offset:], word)
			if pos < 0 {
				break
			}
			start := offset + pos
			end := start + len(word)
			ranges = append(ranges, Range{utf8.RuneCountInString(lower[:start]), utf8.RuneCountInString(lower[:end])})
			offset = end
		}
	}
	return ranges
}
