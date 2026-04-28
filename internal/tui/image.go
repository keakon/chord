package tui

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// maxImageBytes is the maximum allowed image size after any compression.
	maxImageBytes = 5 * 1024 * 1024 // 5 MB
)

// compressIfPNG attempts to convert a PNG to JPEG (quality 85) when that
// yields a smaller file. For non-PNG inputs the data is returned unchanged.
func compressIfPNG(data []byte, mimeType string) ([]byte, string) {
	if mimeType != "image/png" {
		return data, mimeType
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		// Not a decodable PNG – keep as-is.
		return data, mimeType
	}

	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		return data, mimeType
	}

	if jpegBuf.Len() < len(data) {
		return jpegBuf.Bytes(), "image/jpeg"
	}
	return data, mimeType
}

// checkImageSize returns an error if the image exceeds maxImageBytes.
func checkImageSize(data []byte) error {
	if len(data) > maxImageBytes {
		return fmt.Errorf("image too large (%.1f MB), max allowed %d MB",
			float64(len(data))/1024/1024, maxImageBytes/1024/1024)
	}
	return nil
}

// readImageFromClipboard reads an image from the system clipboard.
// Returns the raw bytes and MIME type, or an error.
func readImageFromClipboard() ([]byte, string, error) {
	switch runtime.GOOS {
	case "darwin":
		return clipboardImageDarwin()
	case "linux":
		return clipboardImageLinux()
	default:
		return nil, "", fmt.Errorf("clipboard image not supported on this platform: %s", runtime.GOOS)
	}
}

// clipboardImageDarwin reads a PNG image from macOS clipboard using osascript.
func clipboardImageDarwin() ([]byte, string, error) {
	// Write clipboard image to a temp file via AppleScript.
	tmpFile, err := os.CreateTemp("", "chord-clip-*.png")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	script := fmt.Sprintf(`
set imgFile to open for access POSIX file %q with write permission
set eof imgFile to 0
write (the clipboard as «class PNGf») to imgFile
close access imgFile
`, tmpPath)

	cmd := exec.Command("osascript", "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("no image in clipboard (osascript: %s)", strings.TrimSpace(stderr.String()))
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil || len(data) == 0 {
		return nil, "", fmt.Errorf("no image in clipboard")
	}

	data, mimeType := compressIfPNG(data, "image/png")
	if err := checkImageSize(data); err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

// clipboardImageLinux reads a PNG image from X11/Wayland clipboard.
func clipboardImageLinux() ([]byte, string, error) {
	// Try xclip (X11) first.
	out, err := outputWithoutExternalCommandStderr(exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o"))
	if err == nil && len(out) > 0 {
		data, mimeType := compressIfPNG(out, "image/png")
		if err := checkImageSize(data); err != nil {
			return nil, "", err
		}
		return data, mimeType, nil
	}
	// Try wl-paste (Wayland).
	out, err = outputWithoutExternalCommandStderr(exec.Command("wl-paste", "--type", "image/png"))
	if err == nil && len(out) > 0 {
		data, mimeType := compressIfPNG(out, "image/png")
		if err := checkImageSize(data); err != nil {
			return nil, "", err
		}
		return data, mimeType, nil
	}
	return nil, "", fmt.Errorf("no image in clipboard (requires xclip or wl-paste)")
}

// readImageFile reads an image from the given file path.
// Supported formats: PNG, JPEG.
func readImageFile(path string) ([]byte, string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	var mimeType string
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	default:
		return nil, "", fmt.Errorf("unsupported image format %q, only PNG/JPEG are supported", ext)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	data, mimeType = compressIfPNG(data, mimeType)
	if err := checkImageSize(data); err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}
