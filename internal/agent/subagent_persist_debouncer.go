package agent

import (
	"sync"
	"time"

	"github.com/keakon/golog/log"
)

// subAgentPersistFlushDelay coalesces sub-agent meta/registry writes. Worker
// progress and state events arrive in bursts; each one used to rewrite the
// meta file and the whole task registry synchronously on the event loop,
// stalling dispatch behind multiple disk writes plus a full-registry clone
// per notify. The flush runs on a timer goroutine through mutex-protected
// write paths (persistSubAgentMeta, persistTaskRegistry), which sub
// goroutines already call today.
const subAgentPersistFlushDelay = 200 * time.Millisecond

// subAgentPersistDebouncer tracks dirty persistence targets and fires one
// debounced flush. It holds no agent state of its own beyond dirty markers;
// the agent supplies the fire callback and the current session directory.
type subAgentPersistDebouncer struct {
	fire       func()
	sessionDir func() string

	mu            sync.Mutex
	timer         *time.Timer
	dirtySubs     map[string]*SubAgent
	registryDirty bool
	// pendingDir is the session directory the pending batch was armed under.
	// The flush writes only to it, so a session switch landing inside the
	// debounce window cannot rewrite the previous session's meta files into the
	// successor's directory.
	pendingDir string
}

func newSubAgentPersistDebouncer(fire func(), sessionDir func() string) *subAgentPersistDebouncer {
	return &subAgentPersistDebouncer{
		fire:       fire,
		sessionDir: sessionDir,
		dirtySubs:  make(map[string]*SubAgent),
	}
}

// markSubDirty schedules a debounced meta-file write for the sub.
func (d *subAgentPersistDebouncer) markSubDirty(sub *SubAgent) {
	if d == nil || sub == nil {
		return
	}
	d.mu.Lock()
	d.dirtySubs[sub.instanceID] = sub
	d.armLocked()
	d.mu.Unlock()
}

// markRegistryDirty schedules a debounced task-registry rewrite.
func (d *subAgentPersistDebouncer) markRegistryDirty() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.registryDirty = true
	d.armLocked()
	d.mu.Unlock()
}

func (d *subAgentPersistDebouncer) armLocked() {
	if d.pendingDir == "" && d.sessionDir != nil {
		d.pendingDir = d.sessionDir()
	}
	if d.timer != nil {
		return
	}
	d.timer = time.AfterFunc(subAgentPersistFlushDelay, func() {
		d.mu.Lock()
		d.timer = nil
		d.mu.Unlock()
		if d.fire != nil {
			d.fire()
		}
	})
}

// takePending returns and clears the pending dirty state together with the
// session directory it was armed under.
func (d *subAgentPersistDebouncer) takePending() (map[string]*SubAgent, bool, string) {
	if d == nil {
		return nil, false, ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	subs := d.dirtySubs
	registryDirty := d.registryDirty
	sessionDir := d.pendingDir
	d.dirtySubs = make(map[string]*SubAgent)
	d.registryDirty = false
	d.pendingDir = ""
	return subs, registryDirty, sessionDir
}

// reset drops everything pending and stops the timer. Session switches call
// this: a late flush must never write the previous session's meta files into
// the new session directory.
func (d *subAgentPersistDebouncer) reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.dirtySubs = make(map[string]*SubAgent)
	d.registryDirty = false
	d.pendingDir = ""
	d.mu.Unlock()
}

// syncSubAgentPersists updates the task record in memory and marks the meta
// file and (when the record changed) the registry dirty for the debounced
// flush. Non-terminal progress/state paths call this instead of writing to
// disk inline.
func (a *MainAgent) syncSubAgentPersists(sub *SubAgent, closedReason string) {
	if a == nil || sub == nil {
		return
	}
	changed := a.updateTaskRecordFromSub(sub, closedReason)
	a.subPersists.markSubDirty(sub)
	if changed {
		a.subPersists.markRegistryDirty()
	}
}

// flushDirtySubPersists writes everything the debouncer marked dirty. It runs
// on the timer goroutine; every write below is already called from sub
// goroutines elsewhere, so this stays inside the established concurrency
// envelope.
func (a *MainAgent) flushDirtySubPersists() {
	if a == nil || a.shuttingDown.Load() {
		return
	}
	a.flushPendingSubPersists()
}

// flushPendingSubPersists writes the pending debounced state to the session
// directory it was armed under. A batch armed for another directory belongs to
// a session the agent has already left (the switch dropped its owner), so
// writing it now would recreate that session's meta files under the successor
// session. Shutdown calls this before marking itself shutting down, which is
// what closes the flush on the normal exit path.
func (a *MainAgent) flushPendingSubPersists() {
	subs, registryDirty, sessionDir := a.subPersists.takePending()
	if len(subs) == 0 && !registryDirty {
		return
	}
	if sessionDir == "" || sessionDir != a.SessionDir() {
		log.Debugf("dropping debounced sub-agent persists from an inactive session pending_dir=%v current_dir=%v", sessionDir, a.SessionDir())
		return
	}
	// Serialize against the synchronous writers (persistSubAgentMeta and the
	// registry writer both take this): the meta file is rewritten in place
	// rather than renamed, so a timer-goroutine write racing the event loop's
	// terminal write can both interleave bytes and land last with a stale
	// non-terminal state — after which nothing writes the file again. The lock
	// is released before the registry write, which takes it itself.
	a.subAgentMetaPersistMu.Lock()
	for _, sub := range subs {
		if err := a.persistSubAgentMetaToSession(sub, sessionDir); err != nil {
			log.Warnf("failed to persist subagent meta agent=%v error=%v", sub.instanceID, err)
		}
	}
	a.subAgentMetaPersistMu.Unlock()
	if registryDirty {
		if err := a.persistTaskRegistry(); err != nil {
			log.Warnf("failed to persist durable task registry session=%v error=%v", sessionDir, err)
		}
	}
}
