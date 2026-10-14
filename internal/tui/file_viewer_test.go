package tui

import "testing"

func TestFileViewerDiscardsOldGeneration(t *testing.T) {
	m := Model{}
	m.fileViewer = fileViewerState{open: true, generation: 2, owner: m.imageViewerOwner()}
	m.handleFileViewerLoaded(fileViewerLoadedMsg{request: fileViewerRequest{generation: 1, owner: m.imageViewerOwner()}, Raw: []byte("old"), Lines: []string{"old"}})
	if len(m.fileViewer.raw) > 0 || len(m.fileViewer.lines) > 0 {
		t.Fatal("stale generation installed")
	}
}

func TestFileViewerLinkRequestsShareLatestWorkerSlot(t *testing.T) {
	m := Model{fileViewerBusy: true}
	if cmd := m.openLocalFileLinks([]localFileLink{{path: "/first"}}); cmd != nil {
		t.Fatal("started concurrent worker")
	}
	if cmd := m.openLocalFileLinks([]localFileLink{{path: "/latest"}}); cmd != nil {
		t.Fatal("started concurrent worker")
	}
	if m.fileViewerNext == nil || m.fileViewerNext.links[0].path != "/latest" {
		t.Fatal("waiting slot did not retain latest request")
	}
}

func TestFileViewerMenuKeepsSelectionVisible(t *testing.T) {
	for _, rows := range []int{1, 2} {
		start := fileMenuStart(15, 7, rows)
		if start > 15 || (15-start)*rows+1 >= 7 {
			t.Fatal("selection outside viewport")
		}
	}
	links := localFileLinks("![local](images/plot.png) ![remote](https://example.org/x.png)", "/report")
	if len(links) != 1 || links[0].path != "/report/images/plot.png" {
		t.Fatalf("image references: %#v", links)
	}
}
