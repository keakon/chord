package requesttrim

import (
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// hasImageParts reports whether the parts carry at least one image part. PDFs
// are excluded: they have no re-load path (the read tool rejects them), so
// evicting them would be an unrecoverable loss at any age.
func hasImageParts(parts []message.ContentPart) bool {
	for i := range parts {
		if parts[i].Type == message.ContentPartImage {
			return true
		}
	}
	return false
}

// NextImageReviewAge keeps the image frontier pending until the parts are
// actually removed, including requests where cache amortization defers eviction.
func NextImageReviewAge(ctx Context) int {
	if hasImageParts(ctx.Parts) {
		return ctx.Policy.StaleAgeTurns
	}
	return 0
}

// countImageParts returns how many image parts eviction would drop.
func countImageParts(parts []message.ContentPart) int {
	count := 0
	for i := range parts {
		if parts[i].Type == message.ContentPartImage {
			count++
		}
	}
	return count
}

func imagesRecoverable(ctx Context) bool {
	if !ctx.ImageRecoverable {
		return false
	}
	found := false
	for i := range ctx.Parts {
		if part := &ctx.Parts[i]; part.Type == message.ContentPartImage {
			found = true
			if part.ImagePath == "" {
				return false
			}
		}
	}
	return found
}

// RenderImageEviction returns the text part that replaces a tool result's
// evicted image parts. It is called only when Classify set EvictImages, so a
// missing image part is a caller bug, not a rendering failure — but it still
// reports ok=false rather than inventing a marker for parts that do not exist.
//
// The recall address appears only when re-loading is genuinely available
// (view_image exposed to this agent and the blob persisted on disk); without a
// real recovery route the marker says so instead of pointing at a path the
// model cannot follow.
func RenderImageEviction(ctx Context) (string, bool) {
	evicted := countImageParts(ctx.Parts)
	if evicted == 0 {
		return "", false
	}
	marker := "[Older image output omitted from this request to save context."
	if evicted > 1 {
		marker = "[Older image outputs omitted from this request to save context."
	}
	paths := 0
	if ctx.ImageRecoverable {
		for _, part := range ctx.Parts {
			if part.Type == message.ContentPartImage && part.ImagePath != "" {
				paths++
			}
		}
	}
	if paths > 0 {
		if evicted == 1 {
			for _, part := range ctx.Parts {
				if part.Type == message.ContentPartImage {
					marker += fmt.Sprintf(" The original image is saved at %q; load it back with the view_image tool if needed.", part.ImagePath)
				}
			}
		} else {
			index := 0
			for _, part := range ctx.Parts {
				if part.Type != message.ContentPartImage {
					continue
				}
				index++
				if part.ImagePath != "" {
					marker += fmt.Sprintf(" Image %d is saved at %q.", index, part.ImagePath)
				} else {
					marker += fmt.Sprintf(" Image %d is not re-loadable in this session.", index)
				}
			}
			marker += " Load saved images back with the view_image tool if needed."
			if paths < evicted {
				marker += " To see unsaved images again, re-run the tool that produced them."
			}
		}
	} else if evicted == 1 {
		marker += " It is not re-loadable in this session; re-run the tool that produced it to see it again."
	} else {
		marker += " They are not re-loadable in this session; re-run the tool that produced them to see them again."
	}
	return marker + "]", true
}

// ImagePayloadBytes reports the binary payload size a parts list's image parts
// contribute, for byte accounting in the reduction stats.
func ImagePayloadBytes(parts []message.ContentPart) int64 {
	var total int64
	for i := range parts {
		if parts[i].Type == message.ContentPartImage {
			total += parts[i].PayloadBytes()
		}
	}
	return total
}
