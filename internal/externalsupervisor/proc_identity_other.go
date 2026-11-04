//go:build unix && !linux && !darwin

package externalsupervisor

import "golang.org/x/sys/unix"

// A start token is unavailable on this Unix platform. Callers fall back to
// the pid/pgid check, which is weaker under pid reuse; the receipt keeps an
// empty token and is not treated as cleanup proof for a started process.
func readProcessIdentity(pid int) (pgid int, startToken string, err error) {
	pgid, err = unix.Getpgid(pid)
	return pgid, "", err
}
