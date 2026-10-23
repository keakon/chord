//go:build !unix

package externalsupervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// GroupState is what a bound process-group identity resolves to right now.
type GroupState int

const (
	GroupGone GroupState = iota
	GroupAlive
	// GroupUnknown is the only state a non-Unix platform can prove: without
	// process groups there is no identity to verify and nothing to signal.
	GroupUnknown
)

func (s GroupState) String() string {
	switch s {
	case GroupGone:
		return "gone"
	case GroupAlive:
		return "alive"
	default:
		return "unknown"
	}
}

// ErrUnsupportedProcessGroup reports that managed process groups only exist on
// Unix, so a supervisor cannot own one.
var ErrUnsupportedProcessGroup = errors.New("supervised process groups are supported only on Unix")

// ErrIdentityMismatch reports that a bound pid/pgid pair no longer describes
// the supervised process group.
var ErrIdentityMismatch = errors.New("supervised process identity no longer matches the bound process group")

func configureProcessGroup(_ *exec.Cmd) {}

func processGroupID(_ *exec.Cmd) (int, int, error) {
	return 0, 0, ErrUnsupportedProcessGroup
}

func probeGroup(_, _ int, _ string) GroupState { return GroupUnknown }

func terminateGroup(pid, pgid int, _ string, _ time.Duration) (Cleanup, error) {
	return Cleanup{}, fmt.Errorf("%w: process group %d of pid %d is %s",
		ErrUnsupportedProcessGroup, pgid, pid, probeGroup(pid, pgid, ""))
}

func processStartToken(int) (string, error) { return "", ErrUnsupportedProcessGroup }

func setCloseOnExec(_ ...*os.File) {}

func lifecycleOutputFile(int) (*os.File, error) { return nil, ErrUnsupportedProcessGroup }

func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func exitStatus(err error) (code, signal int) {
	if err == nil {
		return 0, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 1, 0
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return code, 0
	}
	return 1, 0
}
