package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

func readHeadlessStdinLines(ctx context.Context, r io.Reader, out chan<- headlessStdinLine) {
	defer close(out)
	reader := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
			if len(line) > headlessStdinMaxLineBytes {
				discardHeadlessStdinLine(reader, err)
				line = nil
				if !sendHeadlessStdinLine(ctx, out, headlessStdinLine{code: "stdin_line_too_long", err: fmt.Errorf("line exceeds %d bytes", headlessStdinMaxLineBytes)}) {
					return
				}
				if errors.Is(err, io.EOF) {
					return
				}
				continue
			}
		}
		if err == nil {
			line = bytes.TrimSuffix(line, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			// line is reader-owned: the append above copies out of the bufio
			// buffer, and line is reset below, so ownership can move to the
			// consumer without a second copy.
			if !sendHeadlessStdinLine(ctx, out, headlessStdinLine{line: line}) {
				return
			}
			line = nil
			continue
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				line = bytes.TrimSuffix(line, []byte("\r"))
				_ = sendHeadlessStdinLine(ctx, out, headlessStdinLine{line: append([]byte(nil), line...)})
			}
			return
		}
		_ = sendHeadlessStdinLine(ctx, out, headlessStdinLine{err: err, fatal: true})
		return
	}
}

func discardHeadlessStdinLine(reader *bufio.Reader, err error) {
	for err == bufio.ErrBufferFull {
		_, err = reader.ReadSlice('\n')
	}
}

func sendHeadlessStdinLine(ctx context.Context, out chan<- headlessStdinLine, line headlessStdinLine) bool {
	select {
	case out <- line:
		return true
	case <-ctx.Done():
		return false
	}
}
