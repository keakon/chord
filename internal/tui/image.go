package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/keakon/chord/internal/imageutil"
)

// readImageFromClipboard reads an image from the system clipboard.
// Returns the raw bytes and MIME type, or an error.
//
// It is a variable so unit tests can stub it.
var readImageFromClipboard = readImageFromClipboardImpl

func readImageFromClipboardImpl() ([]byte, string, error) {
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

	data, mimeType := imageutil.CompressIfPNG(data, "image/png")
	if err := imageutil.CheckImageSize(data); err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

// clipboardImageLinux reads a PNG image from X11/Wayland clipboard.
func clipboardImageLinux() ([]byte, string, error) {
	// Try xclip (X11) first.
	out, err := outputWithoutExternalCommandStderr(exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o"))
	if err == nil && len(out) > 0 {
		data, mimeType := imageutil.CompressIfPNG(out, "image/png")
		if err := imageutil.CheckImageSize(data); err != nil {
			return nil, "", err
		}
		return data, mimeType, nil
	}
	// Try wl-paste (Wayland).
	out, err = outputWithoutExternalCommandStderr(exec.Command("wl-paste", "--type", "image/png"))
	if err == nil && len(out) > 0 {
		data, mimeType := imageutil.CompressIfPNG(out, "image/png")
		if err := imageutil.CheckImageSize(data); err != nil {
			return nil, "", err
		}
		return data, mimeType, nil
	}
	return nil, "", fmt.Errorf("no image in clipboard (requires xclip or wl-paste)")
}
