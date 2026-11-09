//go:build darwin

package buildcache

import "golang.org/x/sys/unix"

func systemBootID() string {
	id, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return ""
	}
	return id
}
