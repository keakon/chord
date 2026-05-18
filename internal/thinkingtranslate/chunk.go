package thinkingtranslate

import "strings"

func splitIntoChunks(s string, maxChars int) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if maxChars <= 0 {
		return []string{s}
	}
	paras := strings.Split(s, "\n\n")
	out := make([]string, 0, len(paras))
	var buf string
	flush := func() {
		if strings.TrimSpace(buf) != "" {
			out = append(out, buf)
		}
		buf = ""
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		candidate := p
		if buf != "" {
			candidate = buf + "\n\n" + p
		}
		if len([]rune(candidate)) <= maxChars {
			buf = candidate
			continue
		}
		flush()
		if len([]rune(p)) > maxChars {
			lines := strings.Split(p, "\n")
			var lbuf string
			for _, line := range lines {
				line = strings.TrimRight(line, " ")
				cand := line
				if lbuf != "" {
					cand = lbuf + "\n" + line
				}
				if len([]rune(cand)) <= maxChars {
					lbuf = cand
					continue
				}
				if lbuf != "" {
					out = append(out, lbuf)
					lbuf = ""
				}
				if len([]rune(line)) <= maxChars {
					lbuf = line
				} else {
					runes := []rune(line)
					for i := 0; i < len(runes); i += maxChars {
						j := i + maxChars
						if j > len(runes) {
							j = len(runes)
						}
						out = append(out, string(runes[i:j]))
					}
				}
			}
			if lbuf != "" {
				out = append(out, lbuf)
			}
		} else {
			buf = p
		}
	}
	flush()
	if len(out) == 0 {
		return []string{s}
	}
	return out
}
