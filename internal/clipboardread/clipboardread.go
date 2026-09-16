// Package clipboardread reads an image or PDF attachment from the system
// clipboard.
//
// macOS reads the pasteboard through a short-lived `osascript` process running
// the JXA program below: the Go clipboard libraries load AppKit while their
// package initializes and never unload it, which costs every session its
// resident pages whether or not an attachment is ever pasted. The other
// platforms keep reading in-process through the clipboard library
// (clipboardread_library.go), which has no such cost there.
package clipboardread

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/imageutil"
)

// ErrNoAttachment reports a clipboard that holds neither an image nor a PDF.
var ErrNoAttachment = errors.New("no image or PDF found in clipboard")

// readerProtocolMagic starts the payload the JXA reader prints: the magic line,
// then the MIME type (`NONE` when nothing matched), then the payload length in
// bytes, then the base64 payload itself — omitted when the length exceeds the
// limit below, so an oversized attachment never crosses the process boundary.
const readerProtocolMagic = "CHORD-CLIPBOARD/1"

// maxAttachmentBytes bounds the payload the reader sends back. Sizing the limit
// into the script keeps one oversized pasteboard item from being buffered as
// base64 in the reader, piped through the main process, and only then rejected.
const maxAttachmentBytes = max(imageutil.MaxPDFBytes, imageutil.MaxClipboardImageSourceBytes)

// readerScript is the JXA program the darwin read runs. The candidate order is
// the probe order of the in-process read: PDF first, then the image types,
// preferring the pasteboard's native types (com.adobe.pdf, public.png, ...)
// over producers that advertise MIME types verbatim.
var readerScript = fmt.Sprintf(readerScriptTemplate, maxAttachmentBytes)

const readerScriptTemplate = `ObjC.import('AppKit');
ObjC.import('Foundation');

function readAttachment() {
	const maxBytes = %d;
	const pasteboard = $.NSPasteboard.generalPasteboard;
	const types = pasteboard.types;
	const names = [];
	for (let i = 0; i < types.count; i++) {
		names.push(ObjC.unwrap(types.objectAtIndex(i)));
	}
	const candidates = [
		['com.adobe.pdf', 'application/pdf'],
		['application/pdf', 'application/pdf'],
		['public.png', 'image/png'],
		['public.tiff', 'image/tiff'],
		['public.jpeg', 'image/jpeg'],
		['public.webp', 'image/webp'],
		['com.microsoft.bmp', 'image/bmp'],
		['image/png', 'image/png'],
		['image/jpeg', 'image/jpeg'],
		['image/webp', 'image/webp'],
		['image/bmp', 'image/bmp'],
	];
	for (let i = 0; i < candidates.length; i++) {
		const type = candidates[i][0];
		const mime = candidates[i][1];
		if (names.indexOf(type) < 0) {
			continue;
		}
		const data = pasteboard.dataForType(type);
		if (!data) {
			continue;
		}
		const length = Number(data.length);
		if (!length) {
			continue;
		}
		const header = 'CHORD-CLIPBOARD/1\n' + mime + '\n' + length + '\n';
		if (length > maxBytes) {
			return header;
		}
		return header + ObjC.unwrap(data.base64EncodedStringWithOptions(0)) + '\n';
	}
	return 'CHORD-CLIPBOARD/1\nNONE\n0\n';
}

readAttachment();
`

// decodeReaderOutput parses the reader's stdout and returns the raw attachment
// bytes. The declared length is checked against the limit for the reported type
// before the payload is allocated, so a truncated or oversized payload cannot
// make the main process reserve an arbitrary amount of memory.
func decodeReaderOutput(r io.Reader) ([]byte, string, error) {
	lines := bufio.NewReader(r)
	magic, err := readProtocolLine(lines)
	if err != nil {
		return nil, "", err
	}
	if magic != readerProtocolMagic {
		return nil, "", fmt.Errorf("clipboard reader payload starts with %q, want %q", magic, readerProtocolMagic)
	}

	mimeType, err := readProtocolLine(lines)
	if err != nil {
		return nil, "", err
	}
	if mimeType == "NONE" {
		return nil, "", ErrNoAttachment
	}

	lengthLine, err := readProtocolLine(lines)
	if err != nil {
		return nil, "", err
	}
	length, err := strconv.Atoi(strings.TrimSpace(lengthLine))
	if err != nil || length <= 0 {
		return nil, "", fmt.Errorf("clipboard reader reported payload length %q", lengthLine)
	}

	limit := imageutil.MaxClipboardImageSourceBytes
	if mimeType == "application/pdf" {
		limit = imageutil.MaxPDFBytes
	}
	if length > limit {
		return nil, "", fmt.Errorf("clipboard attachment of %.1f MB exceeds the %d MB limit",
			float64(length)/1024/1024, limit/1024/1024)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(base64.NewDecoder(base64.StdEncoding, lines), data); err != nil {
		return nil, "", fmt.Errorf("read clipboard attachment data: %w", err)
	}
	return data, mimeType, nil
}

// readProtocolLine reads one line of the reader's header. osascript appends its
// own newline after the value it prints, so a trailing newline is expected
// rather than an error.
func readProtocolLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read clipboard reader output: %w", err)
	}
	if line == "" {
		return "", errors.New("clipboard reader output ended early")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
