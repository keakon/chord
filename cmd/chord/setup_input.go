package main

import (
	"bufio"
	"io"
	"time"

	uv "github.com/keakon/ultraviolet"
)

// consumeSetupEscape waits for one complete CSI/SS3 sequence. The deadline
// bounds the entire sequence, and non-sequence bytes remain for the next key.
func consumeSetupEscape(reader *bufio.Reader, wait func(time.Duration) (bool, error)) (bool, error) {
	deadline := time.Now().Add(uv.DefaultEscTimeout)
	read := func() (byte, error) {
		if reader.Buffered() == 0 && wait != nil {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, io.EOF
			}
			ready, err := wait(remaining)
			if err != nil {
				return 0, err
			}
			if !ready {
				return 0, io.EOF
			}
		}
		return reader.ReadByte()
	}
	introducer, err := read()
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if introducer != '[' && introducer != 'O' {
		_ = reader.UnreadByte()
		return true, nil
	}
	for {
		b, err := read()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if b >= 0x40 && b <= 0x7e {
			return false, nil
		}
		if b < 0x20 || b > 0x3f {
			_ = reader.UnreadByte()
			return false, nil
		}
	}
}
