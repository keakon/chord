package recovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/keakon/chord/internal/privatefs"
)

// Before appending to a resumed log, remove a torn last line. Otherwise the
// next valid fact would join that line and become unreachable on recovery.
func repairMessageTail(root, path string) error {
	f, err := privatefs.OpenFile(root, path, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	end := info.Size()
	if end == 0 {
		return nil
	}
	var last [1]byte
	if _, err = f.ReadAt(last[:], end-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	offset := end
	var tail []byte
	for offset > 0 {
		start := max(offset-65536, 0)
		chunk := make([]byte, offset-start)
		if _, err = f.ReadAt(chunk, start); err != nil && err != io.EOF {
			return err
		}
		index := bytes.LastIndexByte(chunk, '\n')
		if index >= 0 {
			tail = append(chunk[index+1:], tail...)
			offset = start + int64(index) + 1
			break
		}
		tail = append(chunk, tail...)
		offset = start
		if len(tail) > 64*1024*1024 {
			return fmt.Errorf("unterminated message exceeds recovery limit")
		}
	}
	if json.Valid(tail) {
		if _, err = f.WriteAt([]byte{'\n'}, end); err != nil {
			return err
		}
	} else if err = f.Truncate(offset); err != nil {
		return err
	}
	return f.Sync()
}
