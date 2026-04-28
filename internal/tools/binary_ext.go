package tools

import (
	"path/filepath"
	"strings"
)

// binaryExtensions is the canonical set of file extensions that Chord treats
// as non-referenceable binary content. Shared by:
//   - Grep: fast-path to skip opening the file at all
//   - @-mention file completion in the TUI: exclude from typeahead suggestions
//
// Membership rule: the file format is never useful as source code or prose
// context, and reading its bytes would pollute tool output / the model's
// context with random bytes. Extensions are stored without the leading dot
// and must be compared case-insensitively.
//
// SVG is intentionally omitted: although commonly grouped with images, it is
// XML text and is frequently useful to read.
var binaryExtensions = map[string]struct{}{
	// Compiled bytecode / intermediate object files.
	"pyc": {}, "pyo": {}, "pyd": {},
	"class": {},
	"o":     {}, "obj": {}, "a": {}, "lib": {},
	"node": {}, "wasm": {},

	// Native shared libraries / executables.
	"so": {}, "dylib": {}, "dll": {},
	"exe": {}, "out": {}, "bin": {},

	// Archives.
	"zip": {}, "tar": {}, "tgz": {}, "gz": {}, "bz2": {}, "tbz": {},
	"xz": {}, "txz": {}, "7z": {}, "rar": {},
	"jar": {}, "war": {}, "ear": {}, "apk": {}, "ipa": {}, "dmg": {}, "iso": {},
	"whl": {}, "egg": {},

	// Databases.
	"db": {}, "sqlite": {}, "sqlite3": {}, "mdb": {}, "dbf": {},

	// Images (raster). SVG omitted — it's XML text.
	"png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "bmp": {},
	"tiff": {}, "tif": {}, "webp": {}, "ico": {},
	"heic": {}, "heif": {}, "avif": {}, "psd": {}, "ai": {}, "raw": {},

	// Audio.
	"mp3": {}, "wav": {}, "flac": {}, "ogg": {}, "m4a": {}, "aac": {}, "opus": {},

	// Video.
	"mp4": {}, "mov": {}, "avi": {}, "mkv": {}, "webm": {}, "flv": {}, "wmv": {}, "m4v": {},

	// Fonts.
	"ttf": {}, "otf": {}, "woff": {}, "woff2": {}, "eot": {},

	// Binary office / document containers.
	"pdf": {}, "doc": {}, "docx": {}, "xls": {}, "xlsx": {}, "ppt": {}, "pptx": {},
	"odt": {}, "ods": {}, "odp": {},

	// Misc.
	"pack": {}, "idx": {}, "blob": {},
}

// IsBinaryExtension reports whether the given filename has an extension that
// Chord always treats as binary (non-text, non-referenceable). Comparison is
// case-insensitive. Files with no extension return false — they need a real
// content sniff (via looksBinary) to decide.
func IsBinaryExtension(name string) bool {
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	ext = strings.ToLower(ext[1:])
	_, ok := binaryExtensions[ext]
	return ok
}
