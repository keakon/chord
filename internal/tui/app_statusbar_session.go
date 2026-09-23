package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/pathutil"
)

// statusBarPathLabelSeparator joins the repository name and the checkout name
// in the identity label. It must not read as a path separator: the label
// identifies the checkout, while the checkout itself lives outside the
// repository (the click value still carries that real path).
const statusBarPathLabelSeparator = " · "

// statusBarPathForms carries both halves of the path region: the value a click
// copies (the home-abbreviated checkout path) and the identity that decides how
// the region renders.
type statusBarPathForms struct {
	value    string
	repo     string
	checkout string
}

// display renders the region within maxWidth. A managed checkout shows its
// identity label instead of a path: the checkout lives outside the repository,
// so an abbreviated path either loses the repository name or implies a
// hierarchy that does not exist. The label never degrades into a path — it
// drops the repository name first, then hides once the checkout name no longer
// fits, because an abbreviated identifier is a different, misleading name.
func (f statusBarPathForms) display(maxWidth int) string {
	if maxWidth <= 0 || f.value == "" {
		return ""
	}
	if f.checkout == "" {
		return truncateMiddleDisplay(f.value, maxWidth)
	}
	if f.repo == "" {
		if runewidth.StringWidth(f.checkout) <= maxWidth {
			return f.checkout
		}
		return ""
	}
	full := f.repo + statusBarPathLabelSeparator + f.checkout
	if runewidth.StringWidth(full) <= maxWidth {
		return full
	}
	if runewidth.StringWidth(f.checkout) <= maxWidth {
		return f.checkout
	}
	return ""
}

// workDirRepoShortName is the repository name shown in the checkout identity
// label. It is empty unless a managed worktree is active, so a plain session
// keeps rendering the real path.
func (m *Model) workDirRepoShortName() string {
	if m == nil || m.agent == nil || m.workingDirID == "" {
		return ""
	}
	root := strings.TrimSpace(m.agent.ContentRoot())
	if root == "" {
		return ""
	}
	name := filepath.Base(root)
	if name == "." || name == string(os.PathSeparator) {
		return ""
	}
	return name
}

func displayWorkingDirForHome(path, home string) string {
	return pathutil.AbbreviateHomeIn(path, home)
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
