package clipboardread

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imageutil"
)

// readerOutput builds the payload the JXA reader prints for one attachment, the
// way osascript emits it: the base64 body, then a newline of its own after the
// value the script returned.
func readerOutput(mimeType string, data []byte) string {
	if mimeType == "NONE" {
		return readerProtocolMagic + "\nNONE\n0\n"
	}
	header := readerProtocolMagic + "\n" + mimeType + "\n" + strconv.Itoa(len(data)) + "\n"
	return header + base64.StdEncoding.EncodeToString(data) + "\n\n"
}

func TestDecodeReaderOutputReadsPayload(t *testing.T) {
	payload := []byte("an attachment payload")
	got, mimeType, err := decodeReaderOutput(strings.NewReader(readerOutput("image/tiff", payload)))
	if err != nil {
		t.Fatalf("decodeReaderOutput: %v", err)
	}
	if mimeType != "image/tiff" || !bytes.Equal(got, payload) {
		t.Fatalf("decodeReaderOutput = %d bytes, %q; want %d bytes, image/tiff", len(got), mimeType, len(payload))
	}
}

func TestDecodeReaderOutputReportsNoAttachment(t *testing.T) {
	if _, _, err := decodeReaderOutput(strings.NewReader(readerOutput("NONE", nil))); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("decodeReaderOutput error = %v, want %v", err, ErrNoAttachment)
	}
}

func TestDecodeReaderOutputRejectsOversizedPayload(t *testing.T) {
	// The reader writes only the header once the payload exceeds the limit, so
	// the decode fails on the declared length without waiting for a body.
	header := fmt.Sprintf("%s\napplication/pdf\n%d\n", readerProtocolMagic, imageutil.MaxPDFBytes+1)
	if _, _, err := decodeReaderOutput(strings.NewReader(header)); err == nil {
		t.Fatal("decodeReaderOutput accepted an oversized payload")
	}
}

func TestDecodeReaderOutputRejectsMalformedHeader(t *testing.T) {
	cases := map[string]string{
		"empty output":      "",
		"wrong magic":       "CHORD-CLIPBOARD/2\nimage/png\n1\nAQ==\n",
		"missing length":    readerProtocolMagic + "\nimage/png\n",
		"bad length":        readerProtocolMagic + "\nimage/png\none\nAQ==\n",
		"zero length":       readerProtocolMagic + "\nimage/png\n0\n",
		"truncated payload": readerProtocolMagic + "\nimage/png\n8\nAQ==\n",
		"oversized image":   fmt.Sprintf("%s\nimage/png\n%d\n", readerProtocolMagic, imageutil.MaxImageSourceBytes+1),
		"unpadded base64":   readerProtocolMagic + "\nimage/png\n2\nAQ\n",
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			data, _, err := decodeReaderOutput(strings.NewReader(output))
			if err == nil {
				t.Fatalf("decodeReaderOutput accepted %q as %d bytes", output, len(data))
			}
		})
	}
}

func TestReaderScriptCarriesTheSizeLimit(t *testing.T) {
	if strings.Contains(readerScript, "%!") {
		t.Fatalf("reader script has a stray format verb: %s", readerScript)
	}
	if !strings.Contains(readerScript, "const maxBytes = "+strconv.Itoa(maxAttachmentBytes)+";") {
		t.Fatalf("reader script does not carry the %d byte limit", maxAttachmentBytes)
	}
}
