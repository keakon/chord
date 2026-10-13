package takeover

import (
	"bytes"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestFramesRejectOversizeMalformedAndUnknownFields(t *testing.T) {
	for _, frame := range []string{strings.Repeat("x", MaxFrameBytes) + "\n", `{"protocol":1,"unexpected":true}` + "\n", `{"protocol":1} {"protocol":1}` + "\n", "{\n"} {
		var req request
		if err := readFrame(strings.NewReader(frame), &req); err == nil {
			t.Fatalf("accepted malformed frame length=%d", len(frame))
		}
	}
	var buf bytes.Buffer
	if err := writeFrame(&buf, map[string]string{"data": strings.Repeat("x", MaxFrameBytes)}); err == nil || buf.Len() != 0 {
		t.Fatal("oversize response was written")
	}
}

func TestPublicStatusMatchesGatewayFieldNames(t *testing.T) {
	status := PublicStatus(agent.SessionPauseStatus{PendingInputCount: 3, Error: "error detail", Blockers: []agent.SessionPauseBlocker{{Kind: "tool", Description: "Running tool"}}})
	var buf bytes.Buffer
	if err := writeFrame(&buf, status); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"pending_inputs":3`, `"message":"error detail"`, `"type":"tool"`} {
		if !strings.Contains(buf.String(), field) {
			t.Fatalf("missing gateway field %s: %s", field, buf.String())
		}
	}
}

func TestEscapedBlockersStayInsideWireLimit(t *testing.T) {
	var blockers []agent.SessionPauseBlocker
	for range 130 {
		blockers = append(blockers, agent.SessionPauseBlocker{OwnerTaskID: strings.Repeat("\x00", 128), Kind: strings.Repeat("\x00", 64), Description: strings.Repeat("\x00", 256)})
	}
	status := agent.SessionPauseStatus{Blockers: blockers, BlockerCount: len(blockers)}
	for offset := 0; ; {
		page := pageStatus(status, offset)
		var buf bytes.Buffer
		if err := writeFrame(&buf, response{Protocol: ProtocolVersion, Status: &page}); err != nil {
			t.Fatal(err)
		}
		if len(page.Blockers) == 0 || len(page.Blockers) > MaxBlockers || page.BlockerCount != len(blockers) {
			t.Fatalf("invalid page %+v", page)
		}
		if page.NextOffset == 0 {
			if offset+len(page.Blockers) != len(blockers) {
				t.Fatal("page lost blockers")
			}
			break
		}
		offset = page.NextOffset
	}
}
