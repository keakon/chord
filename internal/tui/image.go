package tui

import (
	"errors"
	"fmt"
	"runtime"
	"slices"

	clipboard "golang.design/x/clipboard"

	"github.com/keakon/chord/internal/imageutil"
)

var errNoClipboardAttachment = errors.New("no image or PDF found in clipboard")

var (
	clipboardInit     = clipboard.Init
	clipboardFormats  = clipboard.Formats
	clipboardRead     = clipboard.Read
	clipboardRegister = clipboard.Register
)

// readAttachmentFromClipboard is a variable so tests can replace the native
// clipboard boundary without touching global OS clipboard state.
var readAttachmentFromClipboard = readAttachmentFromClipboardImpl

func readAttachmentFromClipboardImpl() ([]byte, string, error) {
	if err := clipboardInit(); err != nil {
		return nil, "", fmt.Errorf("clipboard attachment unavailable: %w", err)
	}

	formats := clipboardFormats()
	if clipboardHasMIME(formats, "application/pdf") {
		pdfFormats := []clipboard.Format{clipboardRegister("application/pdf")}
		if runtime.GOOS == "darwin" {
			// AppKit usually advertises PDF data under its native pasteboard UTI,
			// but some producers use the MIME type verbatim.
			pdfFormats = append([]clipboard.Format{clipboardRegister("com.adobe.pdf")}, pdfFormats...)
		}
		for _, pdfFormat := range pdfFormats {
			if data := clipboardRead(pdfFormat); len(data) > 0 {
				if err := imageutil.CheckPDFSize(data); err != nil {
					return nil, "", err
				}
				return data, "application/pdf", nil
			}
		}
	}

	var firstImageErr error
	if clipboardHasFormat(formats, clipboard.FmtImage) {
		if data := clipboardRead(clipboard.FmtImage); len(data) > 0 {
			if normalized, mimeType, err := imageutil.NormalizeClipboardImage(data, "image/png"); err == nil {
				return normalized, mimeType, nil
			} else {
				firstImageErr = err
			}
		}
	}

	for _, mimeType := range []string{"image/png", "image/jpeg", "image/webp", "image/bmp"} {
		if !clipboardHasMIME(formats, mimeType) {
			continue
		}
		if data := clipboardRead(clipboardRegister(mimeType)); len(data) > 0 {
			normalized, normalizedMIME, err := imageutil.NormalizeClipboardImage(data, mimeType)
			if err == nil {
				return normalized, normalizedMIME, nil
			}
			if firstImageErr == nil {
				firstImageErr = err
			}
		}
	}
	if firstImageErr != nil {
		return nil, "", firstImageErr
	}

	return nil, "", errNoClipboardAttachment
}

func clipboardHasFormat(formats []clipboard.Format, target clipboard.Format) bool {
	return slices.Contains(formats, target)
}

func clipboardHasMIME(formats []clipboard.Format, mimeType string) bool {
	for _, format := range formats {
		if format.MIME() == mimeType {
			return true
		}
	}
	return false
}
