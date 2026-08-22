package tui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"time"

	clipboard "golang.design/x/clipboard"

	"github.com/keakon/chord/internal/imageutil"
)

var errNoClipboardAttachment = errors.New("no image or PDF found in clipboard")

// clipboardAttachmentBudget bounds one attachment read attempt. A clipboard read
// waits for the owning application to answer, and the backends cap a single read
// at 5s on their own; without a shared deadline an unresponsive owner would
// multiply that by the number of formats probed below.
const clipboardAttachmentBudget = 3 * time.Second

// clipboard v0.9.0 added a context parameter and error returns to Formats and
// Read. These seams keep the variadic options out of the call sites while
// preserving the error, so a clipboard that cannot be reached stays
// distinguishable from one that simply holds no attachment.
var (
	clipboardInit     = clipboard.Init
	clipboardFormats  = clipboardFormatsImpl
	clipboardRead     = clipboardReadImpl
	clipboardRegister = clipboard.Register
)

func clipboardFormatsImpl(ctx context.Context) ([]clipboard.Format, error) {
	return clipboard.Formats(ctx)
}

func clipboardReadImpl(ctx context.Context, format clipboard.Format) ([]byte, error) {
	return clipboard.Read(ctx, format)
}

// readClipboardData reports whether the read produced data. An empty clipboard
// entry and the ordinary "nothing of this type" error both mean "keep probing";
// any other error means the clipboard itself could not be reached, and it is
// recorded so the user is not told the clipboard was empty.
func readClipboardData(ctx context.Context, format clipboard.Format, firstErr *error) ([]byte, bool) {
	data, err := clipboardRead(ctx, format)
	if err != nil {
		if !errors.Is(err, clipboard.ErrNoData) && *firstErr == nil {
			*firstErr = fmt.Errorf("read clipboard attachment: %w", err)
		}
		return nil, false
	}
	return data, len(data) > 0
}

// readAttachmentFromClipboard is a variable so tests can replace the native
// clipboard boundary without touching global OS clipboard state.
var readAttachmentFromClipboard = readAttachmentFromClipboardImpl

func readAttachmentFromClipboardImpl() ([]byte, string, error) {
	if err := clipboardInit(); err != nil {
		return nil, "", fmt.Errorf("clipboard attachment unavailable: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), clipboardAttachmentBudget)
	defer cancel()

	var firstErr error
	formats, err := clipboardFormats(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read clipboard formats: %w", err)
	}
	if clipboardHasMIME(formats, "application/pdf") {
		pdfFormats := []clipboard.Format{clipboardRegister("application/pdf")}
		if runtime.GOOS == "darwin" {
			// AppKit usually advertises PDF data under its native pasteboard UTI,
			// but some producers use the MIME type verbatim.
			pdfFormats = append([]clipboard.Format{clipboardRegister("com.adobe.pdf")}, pdfFormats...)
		}
		for _, pdfFormat := range pdfFormats {
			if data, ok := readClipboardData(ctx, pdfFormat, &firstErr); ok {
				if err := imageutil.CheckPDFSize(data); err != nil {
					return nil, "", err
				}
				return data, "application/pdf", nil
			}
		}
	}

	if clipboardHasFormat(formats, clipboard.FmtImage) {
		if data, ok := readClipboardData(ctx, clipboard.FmtImage, &firstErr); ok {
			if normalized, mimeType, err := imageutil.NormalizeClipboardImage(data, "image/png"); err == nil {
				return normalized, mimeType, nil
			} else if firstErr == nil {
				firstErr = err
			}
		}
	}

	for _, mimeType := range []string{"image/png", "image/jpeg", "image/webp", "image/bmp"} {
		if !clipboardHasMIME(formats, mimeType) {
			continue
		}
		if data, ok := readClipboardData(ctx, clipboardRegister(mimeType), &firstErr); ok {
			normalized, normalizedMIME, err := imageutil.NormalizeClipboardImage(data, mimeType)
			if err == nil {
				return normalized, normalizedMIME, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return nil, "", firstErr
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
