//go:build windows

package llm

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// codexWindowsVersion reports the NT version the native API returns, in the
// major.minor.build shape the Codex client renders for Windows. RtlGetVersion
// ignores the process manifest, so it names the real installed build.
func codexWindowsVersion() string {
	info := windows.RtlGetVersion()
	if info == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildNumber)
}
