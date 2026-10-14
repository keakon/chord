package tools

import (
	"fmt"
	"unicode/utf8"
)

func decodeToolStringArg(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("tool argument is not valid UTF-8")
	}
	return raw, nil
}

// validateWritableText rejects text that cannot be written to a plain text
// file as-is: NUL and the C0 control characters other than the whitespace set
// ordinary text files use (\t \n \f \r). Tool arguments are JSON strings, so
// models cannot send real binary data — when they emit control characters it
// is malfunction residue, and silently cleaning them could just as well
// corrupt intended content. Rejecting routes the model to a shell command or
// script, which is the correct channel for binary files.
func validateWritableText(s string) error {
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\f' && r != '\r' {
			return fmt.Errorf("contains control character %#U; binary or control-character content cannot be written with the text file tools — write it with a shell command or script instead", r)
		}
	}
	return nil
}
