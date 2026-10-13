package filedelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/keakon/chord/internal/message"
)

// Load reads a bounded prefix while validating an artifact's entire version on
// the same handle. Returned bytes are never installed before validation passes.
func Load(ctx context.Context, declared message.DeliveredFile, session string, limit int64) ([]byte, message.DeliveredFile, bool, error) {
	f, path, err := Open(declared.Locator, session)
	if err != nil {
		return nil, message.DeliveredFile{}, false, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, message.DeliveredFile{}, false, err
	}
	artifact := declared.Locator.Kind == message.FileLocatorArtifact
	if artifact && before.Size() > ArtifactHashLimit {
		return nil, message.DeliveredFile{}, false, errors.New("artifact exceeds validation budget")
	}
	hashContent := artifact || before.Size() <= WorkspaceHashLimit
	h := sha256.New()
	buf := make([]byte, 32<<10)
	var prefix []byte
	var total int64
	readLimit := limit + 1
	if hashContent {
		readLimit = before.Size() + 1
	}
	for total < readLimit {
		if err = ctx.Err(); err != nil {
			return nil, message.DeliveredFile{}, false, err
		}
		n, e := f.Read(buf[:min(int64(len(buf)), readLimit-total)])
		total += int64(n)
		if int64(len(prefix)) < limit+1 {
			prefix = append(prefix, buf[:min(int64(n), limit+1-int64(len(prefix)))]...)
		}
		if hashContent {
			h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, message.DeliveredFile{}, false, e
		}
	}
	after, err := f.Stat()
	if err != nil {
		return nil, message.DeliveredFile{}, false, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || (hashContent && total != before.Size()) {
		return nil, message.DeliveredFile{}, false, errors.New("file changed while loading")
	}
	current := declared
	current.SizeBytes = before.Size()
	current.ModTimeUnixNano = before.ModTime().UnixNano()
	current.SHA256 = ""
	current.MIME = DetectMIME(path, prefix[:min(512, len(prefix))])
	if hashContent {
		current.SHA256 = hex.EncodeToString(h.Sum(nil))
	}
	if artifact && (declared.SHA256 == "" || current.SHA256 != declared.SHA256 || current.SizeBytes != declared.SizeBytes) {
		return nil, current, false, errors.New("declared artifact version is unavailable")
	}
	truncated := int64(len(prefix)) > limit
	if truncated {
		prefix = prefix[:limit]
	}
	return prefix, current, truncated, nil
}
