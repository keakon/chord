package tui

import "github.com/keakon/chord/internal/tui/imagecache"

var imageCache = imagecache.New()

// imageSource captures cache inputs without transferring layout state.
func imageSource(part BlockImagePart) imagecache.Source {
	return imagecache.Source{Data: part.Data, Path: part.ImagePath, MIME: part.MimeType, Key: part.cacheKey}
}
