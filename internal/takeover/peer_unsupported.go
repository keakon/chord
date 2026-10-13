//go:build !darwin && !linux

package takeover

import (
	"net"
	"os"
)

func Supported() bool { return false }
func peerUID(*net.UnixConn) (uint32, error) {
	return 0, protocolError(CodeUnsupportedPlatform, "session takeover requires macOS or Linux")
}
func ownedByCurrentUser(os.FileInfo) bool { return false }
