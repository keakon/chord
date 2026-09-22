package agent

import (
	"fmt"
	"strings"
)

// SetStartupConfigIssues records the config-file problems the tolerant loader
// logged and treated as not configured at startup. The agent reports them once
// when the event loop starts so silently dropped values stay visible, pointing
// at `chord doctor config` for the full report. Set from cmd/chord after the
// startup plan resolves; mirrors SetStartupSkippedLockedSessions.
func (a *MainAgent) SetStartupConfigIssues(issues []string) {
	if a == nil {
		return
	}
	a.stateMu.Lock()
	a.startupConfigIssues = append([]string(nil), issues...)
	a.stateMu.Unlock()
}

func (a *MainAgent) consumeStartupConfigIssues() []string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	issues := a.startupConfigIssues
	a.startupConfigIssues = nil
	return issues
}

// startupConfigIssuesNotice renders the one-time toast reporting that the
// config files carried problems which were ignored at startup.
func startupConfigIssuesNotice(count int) string {
	if count == 1 {
		return `config.yaml had 1 problem that was ignored at startup; run "chord doctor config" for details.`
	}
	return fmt.Sprintf(`config.yaml had %d problems that were ignored at startup; run "chord doctor config" for details.`, count)
}

// SetStartupWorkDirNotice records a resume problem the session could not fix
// before the event loop started, so it can be surfaced once as a toast once the
// TUI is attached. Mirrors SetStartupConfigIssues.
func (a *MainAgent) SetStartupWorkDirNotice(notice string) {
	if a == nil {
		return
	}
	a.stateMu.Lock()
	a.startupWorkDirNotice = strings.TrimSpace(notice)
	a.stateMu.Unlock()
}

func (a *MainAgent) consumeStartupWorkDirNotice() string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	notice := a.startupWorkDirNotice
	a.startupWorkDirNotice = ""
	return notice
}
