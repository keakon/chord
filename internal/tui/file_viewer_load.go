package tui

import (
	"context"

	"github.com/keakon/chord/internal/tui/filepreview"
)

type fileViewerRequest struct {
	filepreview.Request
	checkoutGeneration uint64
	links              []localFileLink
	cache              *filepreview.Cache
	ctx                context.Context
	generation         uint64
	owner              imageViewerOwner
}
type fileViewerLoadedMsg struct {
	filepreview.Result
	links   *fileLinksLoadedMsg
	request fileViewerRequest
}

func loadFileViewer(req fileViewerRequest) fileViewerLoadedMsg {
	result := fileViewerLoadedMsg{request: req}
	if len(req.links) > 0 {
		links := loadLocalFileLinks(req)
		result.links = &links
		return result
	}
	result.Result = req.cache.Load(req.ctx, req.Request)
	return result
}
