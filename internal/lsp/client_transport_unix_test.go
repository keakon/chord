//go:build unix

package lsp

import "syscall"

// brokenPipeErrno is what a write to a server's stdio pipe returns once the
// server stopped reading.
const brokenPipeErrno = syscall.EPIPE
