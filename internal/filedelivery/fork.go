package filedelivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/message"
)

// CopyArtifacts copies only still-available declared versions. A missing source
// remains visible as unavailable metadata; a destination failure aborts the fork.
func CopyArtifacts(ctx context.Context, files []message.DeliveredFile, src, dst string) error {
	seen := make(map[string]bool)
	for _, declared := range files {
		if declared.Locator.Kind != message.FileLocatorArtifact || declared.Locator.Artifact == nil {
			continue
		}
		rel := declared.Locator.Artifact.RelPath
		if seen[rel] {
			continue
		}
		if _, err := Verify(ctx, declared, src); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		f, _, err := Open(declared.Locator, src)
		if err != nil {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			f.Close()
			return err
		}
		out, err := os.CreateTemp(filepath.Dir(target), ".delivery-*")
		if err != nil {
			f.Close()
			return err
		}
		temp := out.Name()
		_, copyErr := io.Copy(out, io.LimitReader(f, ArtifactHashLimit+1))
		f.Close()
		err = errors.Join(copyErr, out.Sync(), out.Close())
		if err == nil {
			// Validate the copied bytes, not a later read of the source.
			relative, _ := filepath.Rel(dst, temp)
			loc, _ := ArtifactLocator(filepath.ToSlash(relative))
			snapshot, e := Inspect(ctx, loc, dst, nil)
			if e != nil || snapshot.SHA256 != declared.SHA256 || snapshot.SizeBytes != declared.SizeBytes {
				os.Remove(temp)
				continue
			}
			err = os.Rename(temp, target)
		}
		if err != nil {
			os.Remove(temp)
			return err
		}
		seen[rel] = true
	}
	return nil
}
