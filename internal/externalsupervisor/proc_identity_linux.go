//go:build linux

package externalsupervisor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// /proc/<pid>/stat retains the group and start time until the child is reaped.
func readProcessIdentity(pid int) (pgid int, startToken string, err error) {
	if pid <= 0 {
		return 0, "", fmt.Errorf("invalid pid %d", pid)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", fmt.Errorf("read process %d identity: %w", pid, err)
	}
	// comm may contain spaces and parentheses; fields resume after the last ')'.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, "", fmt.Errorf("parse /proc/%d/stat: missing comm terminator", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	// The first field after comm is state (field 3), so starttime (field 22)
	// is at index 19.
	if len(fields) <= 19 {
		return 0, "", fmt.Errorf("parse /proc/%d/stat: missing start time", pid)
	}
	pgid, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, "", fmt.Errorf("parse /proc/%d/stat process group: %w", pid, err)
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return pgid, "", fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return pgid, "linux:" + fields[19], nil
}
