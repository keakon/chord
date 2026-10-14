package filepreview

const (
	fileViewerReadLimit   = 1 << 20
	fileViewerSourceLines = 20000
	fileViewerLineLimit   = 16 << 10
	// MarkdownLimit bounds both preview rendering and link discovery input.
	MarkdownLimit         = 128 << 10
	fileViewerRenderLines = 10000
	fileViewerRenderBytes = 2 << 20
	fileViewerCacheBytes  = 8 << 20
	fileViewerMaxWidth    = 400
)
