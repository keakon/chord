package lsp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

func TestHandleApplyEditRejectsWithoutReadingOrWriting(t *testing.T) {
	result, err := handleApplyEdit(context.Background(), "", json.RawMessage(`{"edit":{"changes":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := result.(protocol.ApplyWorkspaceEditResult)
	if got.Applied || got.FailureReason != "workspace/applyEdit rejected: no authorized tool operation" {
		t.Fatalf("result = %+v", got)
	}
}

func TestHandleApplyEditRejectsMalformedParams(t *testing.T) {
	result, err := handleApplyEdit(context.Background(), "", json.RawMessage(`{`))
	if err != nil {
		t.Fatal(err)
	}
	got := result.(protocol.ApplyWorkspaceEditResult)
	if got.Applied || got.FailureReason != "workspace/applyEdit rejected: malformed parameters" {
		t.Fatalf("result = %+v", got)
	}
}
