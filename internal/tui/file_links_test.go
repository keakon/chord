package tui

import (
	"testing"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
)

func TestLocalFileLinksRequireExplicitASTAndBoundBase(t *testing.T) {
	source := "[source](src/main.go#L12) `secret.txt` naked.txt [web](https://example.org/x) [bad](command:open) [absolute](/tmp/report.md)"
	links := localFileLinks(source, "/project")
	if len(links) != 2 || links[0].path != "/project/src/main.go" || links[0].line != 12 {
		t.Fatalf("unexpected links: %#v", links)
	}
	old := localFileLinks(source, "")
	if len(old) != 1 || old[0].path != "/tmp/report.md" {
		t.Fatalf("old relative link inferred cwd: %#v", old)
	}
}

func TestDeliveryCardNarrowActionsKeepStableFileIdentity(t *testing.T) {
	block := &Block{DeliveredFiles: []message.DeliveredFile{{ID: "stable-file", DisplayName: "summary.pdf", MIME: "application/pdf", Locator: message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: "/project", Path: "/project/summary.pdf"}}}}}
	lines := block.renderDeliveryCard(40)
	if len(block.deliveryHits) != 4 {
		t.Fatal("missing direct action targets")
	}
	for _, hit := range block.deliveryHits {
		if hit.fileID != "stable-file" || hit.line >= len(lines) || hit.end > 40 {
			t.Fatalf("invalid action hit: %#v", hit)
		}
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 40 {
			t.Fatal("delivery card overflows narrow terminal")
		}
	}
}
