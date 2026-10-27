package tools

import (
	"encoding/json"
	"fmt"
)

// decodeInstructionArgs shares each tool's typed decoder, including aliases.
func decodeInstructionArgs[T any](raw json.RawMessage) (T, error) {
	var a T
	err := json.Unmarshal(unwrapToolArgs(raw), &a)
	if err != nil {
		return a, fmt.Errorf("invalid arguments: %w", err)
	}
	return a, nil
}

// InstructionTargetMode says how a target path names the directory whose rules
// apply. It exists only for the two triggers directory rules have: the session
// directory, and the directory of a file that is being read.
type InstructionTargetMode uint8

const (
	// InstructionTargetContent names a file; the rules come from the directory
	// the file's content actually lives in, which differs from the link's own
	// directory when the file is a symlink.
	InstructionTargetContent InstructionTargetMode = iota
	// InstructionTargetEntry names a file; the rules come from the directory
	// that holds the entry, symlink or not.
	InstructionTargetEntry
	// InstructionTargetDirectory names a directory directly.
	InstructionTargetDirectory
)

type InstructionTarget struct {
	Path string
	Mode InstructionTargetMode
}
