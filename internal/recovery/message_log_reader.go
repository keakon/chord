package recovery

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/chord/internal/message"
)

// recoverySessionJSON decodes the session's own JSONL records back into the
// long-lived message history, so decoded strings must not retain the read
// buffer (CopyString). Records are written by Chord itself, so string content
// is not re-validated beyond what decoding already rejects.
var recoverySessionJSON = sonicjson.Config{
	CopyString: true,
}.Froze()

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
		if err := recoverySessionJSON.Unmarshal(line, msg); err != nil {
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
