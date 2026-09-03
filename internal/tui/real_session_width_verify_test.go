package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/message"
)

// TestRealSessionToolCardWidthVsThinking replays a real chord session (set
// CHORD_VERIFY_SESSION_DIR; skipped otherwise) by reconstructing tui.Blocks
// through the production messagesToBlocks path, then
// renders them at wide terminal widths to verify two things introduced by the
// tool-card width unification fix:
//  1. Tool cards now reach the SAME right edge as
//     thinking cards (they previously stopped ~40 cols short).
//  2. No rendered line of those tool cards overflows the terminal width
//     (content is truncated, not wrapped).
//
// This validates the fix against real conversation data instead of synthetic fixtures.
func TestRealSessionToolCardWidthVsThinking(t *testing.T) {
	// Opt-in only: point CHORD_VERIFY_SESSION_DIR at a real session directory to
	// replay it through the production messagesToBlocks path, e.g.
	//   CHORD_VERIFY_SESSION_DIR=~/.local/state/chord/sessions/<key>/<id> go test ./internal/tui/
	// No session path is baked in, so the check never depends on one developer's
	// local history.
	dir := os.Getenv("CHORD_VERIFY_SESSION_DIR")
	if dir == "" {
		t.Skip("CHORD_VERIFY_SESSION_DIR not set; skipping real-session replay")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("real session dir not present; skipping (%s): %v", dir, err)
	}
	mainPath := filepath.Join(dir, "main.jsonl")
	msgs, err := loadMessagesFromJSONL(mainPath)
	if err != nil {
		t.Fatalf("load %s: %v", mainPath, err)
	}
	t.Logf("loaded %d messages from %s", len(msgs), mainPath)

	nextID := 0
	blocks := messagesToBlocks(msgs, &nextID)
	t.Logf("reconstructed %d blocks", len(blocks))

	widths := []int{160, 200, 240, 290}

	// Collect, per width, the thinking card surface width and tool-card surface
	// widths, plus any overflow.
	var thinkingWidths []int
	for range widths {
		thinkingWidths = append(thinkingWidths, -1)
	}

	type toolSample struct {
		name  string
		width int
		maxW  int
	}
	var toolSamples []toolSample

	for _, w := range widths {
		thinkingMax := -1
		for _, b := range blocks {
			if b == nil {
				continue
			}
			lines := b.Render(w, "")
			blockMax := 0
			for _, ln := range lines {
				cw := ansi.StringWidth(ln)
				if cw > blockMax {
					blockMax = cw
				}
				// Hard overflow: a line wider than the terminal cannot fit.
				if cw > w {
					t.Errorf("block %d (%s) line overflows terminal at width %d: line width %d\n  %q",
						b.ID, blockKind(b), w, cw, truncateForLog(ln))
				}
			}
			switch {
			case b.Type == BlockThinking:
				if blockMax > thinkingMax {
					thinkingMax = blockMax
				}
			case b.Type == BlockToolCall:
				toolSamples = append(toolSamples, toolSample{name: b.ToolName, width: w, maxW: blockMax})
			}
		}
		for i := range widths {
			if widths[i] == w {
				thinkingWidths[i] = thinkingMax
			}
		}
	}

	// Report and assert unification.
	for i, w := range widths {
		tw := thinkingWidths[i]
		var mismatches []string
		for _, s := range toolSamples {
			if s.width != w {
				continue
			}
			if tw < 0 {
				continue
			}
			if s.maxW != tw {
				mismatches = append(mismatches, fmt.Sprintf("%s card=%d thinking=%d", s.name, s.maxW, tw))
			}
		}
		if len(mismatches) > 0 {
			t.Errorf("at width %d, file-tool cards do not reach the same right edge as thinking cards: %s",
				w, strings.Join(mismatches, "; "))
		} else if tw >= 0 {
			t.Logf("PASS width=%d: thinking card right edge=%d cols, all apply_patch/edit/write cards match", w, tw)
		}
	}

	if len(toolSamples) == 0 {
		t.Logf("note: no apply_patch/edit/write tool cards found in this session slice")
	}
}

func blockKind(b *Block) string {
	switch b.Type {
	case BlockThinking:
		return "thinking"
	case BlockToolCall:
		return "toolcall:" + b.ToolName
	case BlockToolResult:
		return "toolresult"
	case BlockAssistant:
		return "assistant"
	case BlockUser:
		return "user"
	default:
		return "other"
	}
}

func truncateForLog(s string) string {
	s = ansi.Strip(s)
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func loadMessagesFromJSONL(path string) ([]message.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	bufSize := 1 << 16
	if info.Size() > int64(bufSize) {
		bufSize = int(info.Size())
	}
	dec := json.NewDecoder(bufio.NewReaderSize(f, bufSize))
	var out []message.Message
	for {
		var m message.Message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// tolerate truncated trailing record like session restore
			break
		}
		out = append(out, m)
	}
	return out, nil
}
