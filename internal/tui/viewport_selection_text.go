package tui

import "strings"

func normalizeLineNumberPrefix(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if m := lineNumPrefixRe.FindStringSubmatch(l); len(m) == 3 {
			suffix := m[2]
			if !strings.HasPrefix(suffix, " ") {
				continue
			}
			trimmed := strings.TrimPrefix(suffix[1:], " ")
			lines[i] = m[1] + "\t" + trimmed
		}
	}
	return strings.Join(lines, "\n")
}

func dedentLines(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	if minIndent <= 0 {
		return s
	}
	for i, l := range lines {
		if len(l) >= minIndent {
			lines[i] = l[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}

func dedentLinesSkipUnindented(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if n == 0 {
			continue
		}
		if minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	if minIndent <= 0 {
		return s
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if n == 0 {
			continue
		}
		if len(l) >= minIndent {
			lines[i] = l[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}
