//go:build unix && !linux && !darwin

package externalsupervisor

// processStartToken is unavailable on this Unix platform. Callers fall back to
// the pid/pgid check, which is weaker under pid reuse; the receipt keeps an
// empty token and is not treated as cleanup proof for a started process.
func processStartToken(int) (string, error) {
	return "", nil
}
