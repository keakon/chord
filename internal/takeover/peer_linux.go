//go:build linux

package takeover

import (
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func Supported() bool { return true }
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		credErr = e
		if e == nil {
			uid = cred.Uid
		}
	}); err != nil {
		return 0, err
	}
	return uid, credErr
}
func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
