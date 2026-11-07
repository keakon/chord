package recovery

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"

	jsonv2 "encoding/json/v2"

	"github.com/keakon/chord/internal/message"
)

// errTruncatedRecord marks a final record that is missing its newline and does
// not decode: the tail of a crash mid-write.
var errTruncatedRecord = errors.New("truncated final record")

// messageLogReader decodes a JSONL message log one line at a time. Splitting
// on newlines keeps the error classes apart: a malformed complete line is a
// mid-file parse error, while only an undecodable unterminated last line is a
// truncated tail.
type messageLogReader struct {
	br   *bufio.Reader
	line []byte // reused across records; decoding copies what it keeps
}

func newMessageLogReader(r io.Reader) *messageLogReader {
	return &messageLogReader{br: bufio.NewReaderSize(r, 64<<10)}
}

// next decodes the next record into msg. It returns io.EOF at a clean end,
// errTruncatedRecord for an undecodable unterminated tail, and the decode error
// for any other malformed line.
func (r *messageLogReader) next(msg *message.Message) error {
	for {
		line, readErr := r.readLine()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		terminated := readErr == nil
		if len(bytes.TrimSpace(line)) == 0 {
			if !terminated {
				return io.EOF
			}
			continue
		}
		// The reader must accept everything the v1 writer emits: tool-call
		// arguments are persisted verbatim and may carry duplicate object
		// members or invalid UTF-8, exactly as the LLM sent them. The two
		// lenient options reproduce the v1 read tolerance for those records
		// (duplicate keys and invalid UTF-8), while raw captures still keep
		// the bytes verbatim for replay. Field-name matching becomes case
		// sensitive, which only matters for hand-edited logs: this reader
		// consumes records written by the same struct tags it decodes into.
		if err := jsonv2.Unmarshal(line, msg, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)); err != nil {
			if !terminated {
				return errTruncatedRecord
			}
			return err
		}
		return nil
	}
}

// readLine returns the next line including its newline, reusing r.line. The
// error is io.EOF when the input ends without a newline.
func (r *messageLogReader) readLine() ([]byte, error) {
	r.line = r.line[:0]
	for {
		chunk, err := r.br.ReadSlice('\n')
		r.line = append(r.line, chunk...)
		if err != bufio.ErrBufferFull {
			return r.line, err
		}
	}
}
