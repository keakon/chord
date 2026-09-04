package agent

import (
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/recovery"
)

// recoveryOwnership pins one recovery manager to the session identity it writes
// for. The manager is replaced without a session change (context compaction
// rewrites the main transcript through a fresh manager) and the session
// identity changes without keeping the manager (a switch closes the old one),
// so neither alone identifies "the manager the live session persists through".
//
// Ownership values are immutable: installing a manager publishes a new value
// instead of mutating the current one, so a reader that captured one keeps a
// consistent (manager, epoch, dir) triple even while the event loop swaps in
// the next session.
type recoveryOwnership struct {
	manager *recovery.RecoveryManager
	epoch   uint64
	dir     string
}

// currentRecoveryOwnership returns the live ownership value, or nil when the
// session has no open manager (between a freeze and the next install, and after
// shutdown). Safe from any goroutine.
func (a *MainAgent) currentRecoveryOwnership() *recoveryOwnership {
	if a == nil {
		return nil
	}
	a.recoveryOwnerMu.RLock()
	defer a.recoveryOwnerMu.RUnlock()
	return a.recoveryOwner
}

// recoveryManager returns the live session's recovery manager, or nil when
// there is none. Callers that belong to a specific session epoch (sub-agents,
// and anything else that outlives a session switch) must use
// recoveryManagerForEpoch instead, so their writes cannot land in the session
// that replaced theirs. Safe from any goroutine.
func (a *MainAgent) recoveryManager() *recovery.RecoveryManager {
	owner := a.currentRecoveryOwnership()
	if owner == nil {
		return nil
	}
	return owner.manager
}

// recoveryEpochNone is the epoch handed to a worker that started with no open
// recovery manager. Session epochs count from zero, so "no session" needs a
// value outside that range instead of a zero that would match the first one.
const recoveryEpochNone = ^uint64(0)

// recoveryManagerForEpoch returns the live recovery manager only while it still
// belongs to the caller's session epoch. A worker that captured work in an
// earlier session gets nil rather than the new session's manager: its messages
// describe a frozen transcript, and appending them to the successor session
// would splice one session's history into another. It deliberately resolves the
// manager late instead of capturing it at spawn time, because compaction
// replaces the manager within the same epoch and a captured pointer would go on
// writing to a closed one. Safe from any goroutine.
func (a *MainAgent) recoveryManagerForEpoch(epoch uint64) *recovery.RecoveryManager {
	if epoch == recoveryEpochNone {
		return nil
	}
	owner := a.currentRecoveryOwnership()
	if owner == nil {
		return nil
	}
	if owner.epoch != epoch {
		log.Debugf("recovery write skipped for replaced session captured_epoch=%v live_epoch=%v live_dir=%v", epoch, owner.epoch, owner.dir)
		return nil
	}
	return owner.manager
}

// recoverySessionEpoch reports the epoch the live manager belongs to, or
// recoveryEpochNone when there is no manager to belong to. Sub-agents capture
// it at spawn time and pass it back on every write.
func (a *MainAgent) recoverySessionEpoch() uint64 {
	owner := a.currentRecoveryOwnership()
	if owner == nil {
		return recoveryEpochNone
	}
	return owner.epoch
}

// installRecoveryManager publishes rm as the live manager for the current
// session identity and returns the value it replaced (whose manager the caller
// still owns and must close). Event-loop only: it reads a.sessionEpoch and the
// session directory, both event-loop owned.
//
// The publish runs under recoverySnapshotMu as well, so it cannot interleave
// with a snapshot build+write: persistSnapshotLocked resolves the manager
// inside that same guard, which is what makes its "the manager cannot be
// swapped between the guard and the write" invariant true rather than assumed.
func (a *MainAgent) installRecoveryManager(rm *recovery.RecoveryManager) *recoveryOwnership {
	if a == nil {
		return nil
	}
	owner := &recoveryOwnership{manager: rm, epoch: a.sessionEpoch, dir: a.SessionDir()}
	a.recoverySnapshotMu.Lock()
	defer a.recoverySnapshotMu.Unlock()
	a.recoveryOwnerMu.Lock()
	defer a.recoveryOwnerMu.Unlock()
	previous := a.recoveryOwner
	a.recoveryOwner = owner
	return previous
}

// clearRecoveryManagerIf drops the live ownership when it still holds manager,
// reporting whether it did. The compare keeps a freeze from unpublishing a
// manager installed after the freeze captured its own. The caller closes the
// manager; unpublishing first is what stops new writers from picking it up.
func (a *MainAgent) clearRecoveryManagerIf(manager *recovery.RecoveryManager) bool {
	if a == nil {
		return false
	}
	a.recoverySnapshotMu.Lock()
	defer a.recoverySnapshotMu.Unlock()
	a.recoveryOwnerMu.Lock()
	defer a.recoveryOwnerMu.Unlock()
	if a.recoveryOwner == nil || a.recoveryOwner.manager != manager {
		return false
	}
	a.recoveryOwner = nil
	return true
}
