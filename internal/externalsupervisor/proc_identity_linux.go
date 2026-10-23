//go:build linux

package externalsupervisor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processStartToken returns a value that changes when the pid is reused. The
// kernel exposes the process start time in field 22 of /proc/<pid>/stat.
func processStartToken(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// comm may contain spaces and parentheses; fields resume after the last ')'.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", fmt.Errorf("parse /proc/%d/stat: missing comm terminator", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	// The first field after comm is state (field 3), so starttime (field 22)
	// is at index 19.
	if len(fields) <= 19 {
		return "", fmt.Errorf("parse /proc/%d/stat: missing start time", pid)
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return "linux:" + fields[19], nil
}
