package imagecache

// Source describes immutable image bytes or a file-backed image. Key pins a
// previously inspected file version for background preparation; an empty Key
// asks Lookup to inspect the source. Data is borrowed and must not be mutated.
type Source struct {
	Data []byte
	Path string
	MIME string
	Key  string
}
