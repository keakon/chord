package tui

import (
	"github.com/mattn/go-runewidth"
	"os"
	"path/filepath"
	"strings"
)

func displayWorkingDirForHome(path, home string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home = strings.TrimSpace(home)
	if home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}

func displayWorkingDir(path string) string {
	home, _ := os.UserHomeDir()
	return displayWorkingDirForHome(path, home)
}

func truncateMiddleDisplay(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 1 {
		return "…"
	}
	if sep := string(os.PathSeparator); strings.Contains(s, sep) {
		if candidate := compactPathDisplay(s); candidate != "" && runewidth.StringWidth(candidate) <= maxWidth {
			return candidate
		}
	}
	runes := []rune(s)
	var leftB strings.Builder
	rightRunes := make([]rune, 0, len(runes))
	leftW := 0
	rightW := 0
	remain := maxWidth - 1
	for l, r := 0, len(runes)-1; l <= r && leftW+rightW < remain; {
		if leftW <= rightW {
			w := runewidth.RuneWidth(runes[l])
			if leftW+rightW+w > remain {
				break
			}
			leftB.WriteRune(runes[l])
			leftW += w
			l++
		} else {
			w := runewidth.RuneWidth(runes[r])
			if leftW+rightW+w > remain {
				break
			}
			rightRunes = append([]rune{runes[r]}, rightRunes...)
			rightW += w
			r--
		}
	}
	return leftB.String() + "…" + string(rightRunes)
}

func compactPathDisplay(path string) string {
	sep := string(os.PathSeparator)
	base := filepath.Base(path)
	prefix := firstPathSegment(path)
	if prefix == "" || base == "." || base == sep {
		return ""
	}
	return prefix + sep + "…" + sep + base
}

func firstPathSegment(path string) string {
	sep := string(os.PathSeparator)
	if path == "" {
		return ""
	}
	trimmed := strings.TrimPrefix(path, sep)
	if trimmed == "" {
		return path
	}
	parts := strings.Split(trimmed, sep)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	if strings.HasPrefix(path, sep) {
		return sep + parts[0]
	}
	return parts[0]
}
