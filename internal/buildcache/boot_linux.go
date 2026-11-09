//go:build linux

package buildcache

import (
	"os"
	"strings"
)

func systemBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
