package tools

import (
	"encoding/json"
	"strings"
)

// EditIntentScanner extracts complete paths from argument fragments. Its output
// is a prewarm hint, never a substitute for execution-time argument validation.
// Only canonical top-level JSON and bare patches are recognized.
type EditIntentScanner struct {
	patch         bool
	mode          byte
	state         byte
	depth         int
	inString      bool
	escaped       bool
	unicodeLeft   int
	unicodeDigits [4]byte
	aliases       map[string]string
	key           string
	token         []byte
	overflow      bool
	line          []byte
	lineOverflow  bool
	begun         bool
	pending       ApplyPatchDisplayTarget
}

const editIntentTokenLimit = 8192

func NewEditIntentScanner(name string) *EditIntentScanner {
	switch NormalizeName(name) {
	case NameApplyPatch:
		return &EditIntentScanner{patch: true}
	case NameEdit:
		return &EditIntentScanner{aliases: (EditTool{}).argumentAliases()}
	case NameWrite:
		return &EditIntentScanner{}
	default:
		return nil
	}
}

func (s *EditIntentScanner) Feed(fragment string, emit func(ApplyPatchDisplayTarget)) {
	for i := 0; i < len(fragment); i++ {
		if s.mode == 'x' {
			return
		}
		c := fragment[i]
		if s.mode == 0 {
			if strings.ContainsRune(" \t\r\n", rune(c)) {
				continue
			}
			switch {
			case c == '{':
				s.mode, s.state = 'j', 'k'
				continue
			case s.patch && c == '*':
				s.mode = 'p'
			default:
				s.mode = 'x'
			}
		}
		if s.mode == 'p' {
			s.patchByte(c, emit)
		} else if s.mode == 'j' {
			s.jsonByte(c, emit)
		}
	}
}

func (s *EditIntentScanner) jsonByte(c byte, emit func(ApplyPatchDisplayTarget)) {
	if s.inString {
		closing := c == '"' && !s.escaped
		capture := s.depth == 0 && (s.state == 'k' || s.key == "path" || s.patch && s.key == "patch")
		if capture && !closing && !s.overflow {
			if len(s.token) == editIntentTokenLimit {
				s.overflow = true
				s.token = s.token[:0]
			} else {
				s.token = append(s.token, c)
			}
		}
		// Decode patch lines independently, so a long body never accumulates
		// in this scanner. Escaped backslashes must not act as line breaks.
		newline := s.escaped && c == 'n'
		if s.unicodeLeft > 0 {
			s.unicodeDigits[4-s.unicodeLeft] = c
			s.unicodeLeft--
			if s.unicodeLeft == 0 {
				newline = s.unicodeDigits == [4]byte{'0', '0', '0', 'a'} || s.unicodeDigits == [4]byte{'0', '0', '0', 'A'}
			}
		} else if s.escaped && c == 'u' {
			s.unicodeLeft = 4
		}
		if s.patch && s.key == "patch" && s.depth == 0 && (newline || closing) {
			if s.overflow && s.pending.SourcePath != "" {
				s.mode = 'x'
				return
			}
			if !s.overflow {
				var text string
				if json.Unmarshal(append(append([]byte{'"'}, s.token...), '"'), &text) != nil {
					s.mode = 'x'
					return
				}
				for j := range len(text) {
					s.patchByte(text[j], emit)
				}
			}
			s.token = s.token[:0]
			s.overflow = false
		}
		if closing {
			if s.depth == 0 {
				if s.state == 'k' {
					if s.overflow || json.Unmarshal(append(append([]byte{'"'}, s.token...), '"'), &s.key) != nil {
						s.mode = 'x'
						return
					}
					s.state = ':'
					if canonical, ok := s.aliases[s.key]; ok {
						s.key = canonical
					}
				} else {
					if !s.patch && s.key == "path" && !s.overflow {
						var path string
						if json.Unmarshal(append(append([]byte{'"'}, s.token...), '"'), &path) == nil && path != "" {
							emit(ApplyPatchDisplayTarget{Kind: MutationUpdate, SourcePath: path})
						}
						// A later duplicate field can change the execution target.
						// Prewarming the first path is harmless; never execute it.
						s.mode = 'x'
					}
					s.state = ','
				}
			}
			s.inString = false
			s.token = s.token[:0]
			s.overflow = false
		}
		if s.escaped {
			s.escaped = false
		} else if c == '\\' {
			s.escaped = true
		}
		return
	}
	if strings.ContainsRune(" \t\r\n", rune(c)) {
		return
	}
	if s.depth > 0 {
		switch c {
		case '"':
			s.inString = true
		case '{', '[':
			s.depth++
		case '}', ']':
			s.depth--
			if s.depth == 0 {
				s.state = ','
			}
		}
		return
	}
	switch s.state {
	case 'k':
		if c != '"' {
			s.mode = 'x'
			return
		}
		s.key = ""
		s.inString = true
	case ':':
		if c != ':' {
			s.mode = 'x'
			return
		}
		s.state = 'v'
	case 'v':
		switch c {
		case '"':
			s.inString = true
		case '{', '[':
			s.depth = 1
		default:
			// Non-string values cannot contain an editing target.
			s.state = 's'
		}
	case 's':
		if c != ',' && c != '}' {
			return
		}
		fallthrough
	case ',':
		if c == ',' {
			s.state = 'k'
		} else {
			s.mode = 'x'
		}
	}
}

func (s *EditIntentScanner) patchByte(c byte, emit func(ApplyPatchDisplayTarget)) {
	if c != '\n' {
		if len(s.line) < editIntentTokenLimit {
			s.line = append(s.line, c)
		} else {
			s.lineOverflow = true
		}
		return
	}
	line := strings.TrimSuffix(string(s.line), "\r")
	s.line = s.line[:0]
	if s.lineOverflow {
		s.lineOverflow = false
		if s.pending.SourcePath != "" {
			s.mode = 'x'
		}
		return
	}
	if !s.begun {
		if line != "*** Begin Patch" {
			s.mode = 'x'
		}
		s.begun = true
		return
	}
	if s.pending.SourcePath != "" {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), ApplyPatchMoveToMarker); ok {
			s.pending.Kind = MutationMove
			s.pending.TargetPath = strings.TrimSpace(path)
		}
		emit(s.pending)
		s.pending = ApplyPatchDisplayTarget{}
	}
	if path, ok := strings.CutPrefix(line, ApplyPatchAddFileMarker); ok {
		emit(ApplyPatchDisplayTarget{Kind: MutationAdd, SourcePath: strings.TrimSpace(path)})
	} else if path, ok := strings.CutPrefix(line, ApplyPatchUpdateFileMarker); ok {
		// Wait one line to distinguish an update from a move, whose source
		// also needs delete permission.
		s.pending = ApplyPatchDisplayTarget{Kind: MutationUpdate, SourcePath: strings.TrimSpace(path)}
	} else if line == "*** End Patch" {
		s.mode = 'x'
	}
}
