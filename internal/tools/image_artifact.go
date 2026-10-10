package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/privatefs"
)

const ImageArtifactPrefix = "artifact:"

func imageDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// publishImageFile uses a synced temporary file and an exclusive hard link.
// Holding a directory root prevents parent-symlink replacement from redirecting
// an already-authorized output into another directory.
func publishImageFile(ctx context.Context, root *os.Root, name string, data []byte) error {
	published := false
	return imagegen.RetryDelivery(ctx, "publish-image-file", func() error {
		if published {
			d, err := root.Open(".")
			if err != nil {
				return err
			}
			defer d.Close()
			return privatefs.SyncDirectory(d)
		}
		var err error
		published, err = publishImageFileOnce(root, name, data)
		return err
	})
}

func publishImageFileOnce(root *os.Root, name string, data []byte) (bool, error) {
	temp := ".image-" + hex.EncodeToString(randomImageID())
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privatefs.FileMode)
	if err != nil {
		return false, fmt.Errorf("create image temporary file: %w", err)
	}
	defer root.Remove(temp)
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("write image temporary file: %w", err)
	}
	if err := root.Link(temp, name); err != nil {
		return false, fmt.Errorf("publish image without overwrite: %w", err)
	}
	d, err := root.Open(".")
	if err != nil {
		return true, fmt.Errorf("open image directory: %w", err)
	}
	defer d.Close()
	return true, privatefs.SyncDirectory(d)
}

func randomImageID() []byte {
	id := make([]byte, 16)
	// Failure to obtain system randomness is fatal in crypto/rand.Read.
	_, _ = rand.Read(id)
	return id
}

// A restored operation may have published its output before saving the final
// receipt. Reuse only a regular file whose pinned identity and bytes agree.
func verifyExistingImageFile(root *os.Root, name string, data []byte) error {
	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect existing image output: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
		return fmt.Errorf("existing image output does not match the saved original")
	}
	f, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("open existing image output: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened image output: %w", err)
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("existing image output changed while opening")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, int64(len(data))+1))
	if err != nil {
		return fmt.Errorf("verify existing image output: %w", err)
	}
	current, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect verified image output: %w", err)
	}
	if n != int64(len(data)) || hex.EncodeToString(h.Sum(nil)) != imageDigest(data) || !os.SameFile(opened, current) {
		return fmt.Errorf("existing image output does not match the saved original")
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync existing image output: %w", err)
	}
	d, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open existing image output directory: %w", err)
	}
	defer d.Close()
	if err := privatefs.SyncDirectory(d); err != nil {
		return fmt.Errorf("sync existing image output directory: %w", err)
	}
	return nil
}

// SaveImageArtifact extends the existing session images directory with
// immutable, content-addressed originals; it does not change text artifacts.
func SaveImageArtifact(ctx context.Context, sessionDir string, img imagegen.Image) (ArtifactRef, error) {
	var ref ArtifactRef
	err := imagegen.RetryDelivery(ctx, "save-original", func() error {
		var err error
		ref, err = saveImageArtifact(sessionDir, img)
		return err
	})
	return ref, err
}

func saveImageArtifact(sessionDir string, img imagegen.Image) (ArtifactRef, error) {
	digest := imageDigest(img.Data)
	dir := filepath.Join(sessionDir, "images")
	if err := privatefs.EnsureDir(sessionDir, dir); err != nil {
		return ArtifactRef{}, fmt.Errorf("prepare image store: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("open image store: %w", err)
	}
	defer root.Close()
	name := "sha256-" + digest + imagegen.Extension(img.MIME)
	if _, err := publishImageFileOnce(root, name, img.Data); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return ArtifactRef{}, err
		}
		f, readErr := root.Open(name)
		if readErr != nil {
			return ArtifactRef{}, fmt.Errorf("read image collision: %w", readErr)
		}
		data, readErr := imagegen.ReadBounded(f, imagegen.MaxImageBytes)
		_ = f.Close()
		if readErr != nil || imageDigest(data) != digest {
			return ArtifactRef{}, fmt.Errorf("immutable image collision")
		}
		d, err := root.Open(".")
		if err != nil {
			return ArtifactRef{}, err
		}
		defer d.Close()
		if err := privatefs.SyncDirectory(d); err != nil {
			return ArtifactRef{}, err
		}
	}
	return imageArtifactRef(img), nil
}

func ResolveImageArtifactPath(ctxDir, path, baseDir string) (string, error) {
	if rel, ok := strings.CutPrefix(path, ImageArtifactPrefix); ok {
		resolved, err := ResolveSessionArtifactPath(ctxDir, rel)
		if err != nil {
			return "", err
		}
		// Keep authorization and reads on the same lexical artifact. A symlink
		// alias within the session must not acquire a different path's approval.
		absolute, err := filepath.Abs(filepath.Join(ctxDir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		f, err := openImageSnapshot(ctxDir, absolute)
		if err != nil {
			return "", err
		}
		_ = f.Close()
		return resolved, nil
	}
	return resolveToolPathAbsInDir(path, baseDir)
}

func imageArtifactRef(img imagegen.Image) ArtifactRef {
	digest := imageDigest(img.Data)
	name := "sha256-" + digest + imagegen.Extension(img.MIME)
	return ArtifactRef{ID: "sha256-" + digest, Type: "generated_image", RelPath: filepath.ToSlash(filepath.Join("images", name)), MimeType: img.MIME, SizeBytes: int64(len(img.Data)), SHA256: digest}
}
