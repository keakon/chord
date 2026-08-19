package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
)

func cloneSessionSummary(summary *SessionSummary) *SessionSummary {
	if summary == nil {
		return nil
	}
	cpy := *summary
	return &cpy
}

func (a *MainAgent) setSessionSummary(summary *SessionSummary) {
	a.stateMu.Lock()
	a.sessionSummary = cloneSessionSummary(summary)
	a.stateMu.Unlock()
}

func (a *MainAgent) consumeStartupResumePending() bool {
	a.stateMu.Lock()
	pending := a.startupResumePending
	a.startupResumePending = false
	a.stateMu.Unlock()
	return pending
}

// SetStartupSkippedLockedSessions records the sessions --continue passed over
// at startup because another live Chord process owned them. The agent reports
// them once when the event loop starts so the fallback is never silent.
func (a *MainAgent) SetStartupSkippedLockedSessions(ids []string) {
	a.stateMu.Lock()
	a.startupSkippedLockedSessions = append([]string(nil), ids...)
	a.stateMu.Unlock()
}

func (a *MainAgent) consumeStartupSkippedLockedSessions() []string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	ids := a.startupSkippedLockedSessions
	a.startupSkippedLockedSessions = nil
	return ids
}

// startupSkippedSessionsNotice renders the one-time toast reporting that
// --continue passed over sessions owned by another live Chord process.
func startupSkippedSessionsNotice(skipped []string) string {
	switch len(skipped) {
	case 1:
		return fmt.Sprintf("Session %s is open in another Chord process; continuing with this session.", skipped[0])
	default:
		return fmt.Sprintf("Sessions %s are open in another Chord process; continuing with this session.", strings.Join(skipped, ", "))
	}
}

func (a *MainAgent) startupResumeSessionIDValue() string {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return strings.TrimSpace(a.startupResumeSessionID)
}

func (a *MainAgent) startupResumeLoadedAtValue() time.Time {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.startupResumeLoadedAt
}

func (a *MainAgent) updateSessionSummary(mut func(*SessionSummary)) {
	if mut == nil {
		return
	}
	a.stateMu.Lock()
	var summary *SessionSummary
	if a.sessionSummary != nil {
		cpy := *a.sessionSummary
		summary = &cpy
	} else if sid := strings.TrimSpace(filepath.Base(a.sessionDir)); sid != "" && sid != "." {
		summary = &SessionSummary{ID: sid, Locked: a.sessionLock != nil}
	}
	mut(summary)
	a.sessionSummary = summary
	a.stateMu.Unlock()
}

func (a *MainAgent) refreshSessionSummary() {
	a.setSessionSummary(buildSessionSummaryForDir(a.sessionDir, a.sessionLock != nil))
}

func buildSessionSummaryForDir(sessionDir string, locked bool) *SessionSummary {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return nil
	}
	summary := &SessionSummary{
		ID:     filepath.Base(sessionDir),
		Locked: locked,
	}
	mainPath := filepath.Join(sessionDir, identity.MainSessionLogFilename)
	if info, err := os.Stat(mainPath); err == nil {
		summary.LastModTime = info.ModTime()
	}
	if ledger := analytics.NewUsageLedger(sessionDir, ""); ledger != nil {
		if usageSummary, err := ledger.Summary(); err == nil && usageSummary != nil {
			if !usageSummary.LastUpdatedAt.IsZero() && usageSummary.LastUpdatedAt.After(summary.LastModTime) {
				summary.LastModTime = usageSummary.LastUpdatedAt
			}
			if usageSummary.FirstUserMessage != "" {
				summary.FirstUserMessage = usageSummary.FirstUserMessage
				summary.FirstUserMessageIsCompactionSummary = usageSummary.FirstUserMessageIsCompactionSummary
			}
			if usageSummary.OriginalFirstUserMessage != "" {
				summary.OriginalFirstUserMessage = usageSummary.OriginalFirstUserMessage
			}
		}
		if summary.OriginalFirstUserMessage == "" && !summary.FirstUserMessageIsCompactionSummary {
			summary.OriginalFirstUserMessage = summary.FirstUserMessage
		}
	}
	if summary.FirstUserMessage == "" {
		if firstUser, err := recovery.FirstUserMessageFromFile(mainPath); err == nil {
			summary.FirstUserMessage = firstUser
		}
	}
	if meta, err := recovery.LoadSessionMeta(sessionDir); err == nil && meta != nil {
		summary.ForkedFrom = meta.ForkedFrom
		summary.Title = meta.Title
	}
	return summary
}
