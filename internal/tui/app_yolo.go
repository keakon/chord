package tui

import (
	"github.com/keakon/chord/internal/agent"
)

func (m *Model) yoloEnabled() bool {
	if m == nil || m.agent == nil {
		return false
	}
	reporter, ok := m.agent.(agent.YoloController)
	if !ok {
		return false
	}
	return reporter.YoloEnabled()
}

func (m *Model) maybeYoloShortcut(key string) bool {
	next := "on"
	if m.yoloEnabled() {
		next = "off"
	}
	return m.sendSlashShortcut(key, m.keyMap.Yolo, "/yolo "+next)
}
