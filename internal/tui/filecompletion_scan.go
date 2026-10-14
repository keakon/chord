package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tui/filecompletion"
)

const (
	atMentionMaxFiles   = 10000
	atMentionRefreshTTL = 5 * time.Second
)

type atMentionFilesLoadedMsg struct {
	files      []string
	workDir    string
	generation uint64
}

func loadAtMentionFilesForWorkDir(workDir string, generation uint64, limit int) tea.Cmd {
	return func() tea.Msg {
		return atMentionFilesLoadedMsg{
			files:      filecompletion.Scan(workDir, limit),
			workDir:    workDir,
			generation: generation,
		}
	}
}

func (m *Model) startAtMentionFileLoad() tea.Cmd {
	return m.startAtMentionFileLoadIfStale(time.Now())
}

func (m *Model) startAtMentionFileLoadIfStale(now time.Time) tea.Cmd {
	if m.atMentionLoading {
		return nil
	}
	if m.atMentionLoaded && now.Sub(m.atMentionLoadedAt) < atMentionRefreshTTL {
		return nil
	}
	m.atMentionLoading = true
	return loadAtMentionFilesForWorkDir(m.workingDir, m.workingDirGeneration, atMentionMaxFiles)
}
