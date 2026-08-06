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

func TestLocationConversions(t *testing.T) {
	uri := protocol.DocumentURI("file:///tmp/example.go")
	loc := protocol.Location{URI: uri, Range: protocol.Range{Start: protocol.Position{Line: 3, Character: 7}}}
	got := locationToRefLocation(loc)
	if got.Path != "/tmp/example.go" || got.Line != 3 || got.Col != 7 {
		t.Fatalf("locationToRefLocation = %+v", got)
	}
	links := definitionLinksToRefLocations([]protocol.DefinitionLink{{
		TargetURI:            uri,
		TargetSelectionRange: protocol.Range{Start: protocol.Position{Line: 4, Character: 2}},
	}})
	if len(links) != 1 || links[0].Line != 4 || links[0].Col != 2 {
		t.Fatalf("definitionLinksToRefLocations = %+v", links)
	}
}
