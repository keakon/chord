package tui

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/chord/internal/tools"
)

type argumentStreamPhase byte

const (
	argumentStreamStart argumentStreamPhase = iota
	argumentStreamKey
	argumentStreamKeyString
	argumentStreamColon
	argumentStreamValue
	argumentStreamScanValue
	argumentStreamAfterValue
	argumentStreamDone
	argumentStreamInvalid
)

// streamingToolArguments reads complete top-level fields for display only.
// The execution payload remains RawArgs; replacements reset all parsed state.
type streamingToolArguments struct {
	raw               string
	readFields        bool
	offset, start     int
	phase             argumentStreamPhase
	key               string
	quoted, escaped   bool
	scalar            bool
	depth             int
	values            map[string]json.RawMessage
	runeOffset, runes int
	provisional       bool
	previous          json.RawMessage
}

func (s *streamingToolArguments) update(raw string) {
	if raw == s.raw {
		return
	}
	if !strings.HasPrefix(raw, s.raw) {
		*s = streamingToolArguments{readFields: s.readFields}
	}
	if s.provisional {
		if s.previous == nil {
			delete(s.values, s.key)
		} else {
			s.values[s.key] = s.previous
		}
		s.provisional = false
		s.previous = nil
	}
	s.raw = raw
	for s.runeOffset < len(raw) && utf8.FullRuneInString(raw[s.runeOffset:]) {
		_, size := utf8.DecodeRuneInString(raw[s.runeOffset:])
		s.runeOffset += size
		s.runes++
	}
	if !s.readFields {
		return
	}
	for s.offset < len(raw) && s.phase != argumentStreamInvalid && s.phase != argumentStreamDone {
		c := raw[s.offset]
		switch s.phase {
		case argumentStreamStart:
			if jsonSpace(c) {
				s.offset++
				continue
			}
			if c != '{' {
				s.phase = argumentStreamInvalid
				continue
			}
			s.phase = argumentStreamKey
		case argumentStreamKey:
			if jsonSpace(c) {
				s.offset++
				continue
			}
			if c == '}' {
				s.phase = argumentStreamDone
				continue
			}
			if c != '"' {
				s.phase = argumentStreamInvalid
				continue
			}
			s.start = s.offset
			s.escaped = false
			s.phase = argumentStreamKeyString
		case argumentStreamKeyString:
			if s.escaped {
				s.escaped = false
			} else if c == '\\' {
				s.escaped = true
			} else if c == '"' {
				if json.Unmarshal([]byte(raw[s.start:s.offset+1]), &s.key) != nil {
					s.phase = argumentStreamInvalid
					continue
				}
				s.phase = argumentStreamColon
			}
		case argumentStreamColon:
			if jsonSpace(c) {
				s.offset++
				continue
			}
			if c != ':' {
				s.phase = argumentStreamInvalid
				continue
			}
			s.phase = argumentStreamValue
		case argumentStreamValue:
			if jsonSpace(c) {
				s.offset++
				continue
			}
			s.start = s.offset
			s.scalar = c != '"' && c != '{' && c != '['
			s.depth = 0
			s.quoted, s.escaped = false, false
			s.phase = argumentStreamScanValue
			continue
		case argumentStreamScanValue:
			if s.scalar {
				if jsonSpace(c) || c == ',' || c == '}' || c == ']' || c == '"' || c == '{' || c == '[' {
					s.settle(s.offset)
					s.phaseAfterValue()
					continue
				}
				break
			}
			if s.quoted {
				if s.escaped {
					s.escaped = false
				} else if c == '\\' {
					s.escaped = true
				} else if c == '"' {
					s.quoted = false
					if s.depth == 0 {
						s.settle(s.offset + 1)
						s.phaseAfterValue()
					}
				}
			} else {
				switch c {
				case '"':
					s.quoted = true
				case '{', '[':
					s.depth++
				case '}', ']':
					if s.depth > 0 {
						s.depth--
						if s.depth == 0 {
							s.settle(s.offset + 1)
							s.phaseAfterValue()
						}
					} else {
						s.settle(s.offset)
						s.phaseAfterValue()
						continue
					}
				case ',', ' ', '\n', '\r', '\t':
					if s.depth == 0 {
						s.settle(s.offset)
						s.phaseAfterValue()
						continue
					}
				}
			}
		case argumentStreamAfterValue:
			if jsonSpace(c) {
				s.offset++
				continue
			}
			switch c {
			case ',':
				s.phase = argumentStreamKey
			case '}':
				s.phase = argumentStreamDone
			default:
				s.phase = argumentStreamInvalid
			}
		}
		s.offset++
	}
	// A scalar can already be readable at EOF but can still grow in the next
	// snapshot (for example 1 followed by 0). Keep scanning it on append.
	if s.phase == argumentStreamScanValue && s.scalar {
		value := raw[s.start:]
		if end, ok := scalarDisplayEnd(value); ok {
			s.previous = s.values[s.key]
			s.provisional = true
			s.store(value[:end])
			if strings.TrimSpace(value[end:]) != "" {
				s.provisional = false
				s.previous = nil
				s.phase = argumentStreamInvalid
			}
		}
	}
}

// Match the tolerant card parser, which can read 0 from an invalid 00 suffix
// but cannot read an unfinished exponent. Never continue past leftover text.
func scalarDisplayEnd(value string) (int, bool) {
	dec := json.NewDecoder(strings.NewReader(value))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return 0, false
	}
	return int(dec.InputOffset()), true
}

func jsonSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

func (s *streamingToolArguments) phaseAfterValue() {
	if s.phase != argumentStreamInvalid {
		s.phase = argumentStreamAfterValue
	}
}

func (s *streamingToolArguments) settle(end int) {
	value := s.raw[s.start:end]
	if s.scalar {
		end, ok := scalarDisplayEnd(value)
		if !ok {
			s.phase = argumentStreamInvalid
			return
		}
		s.store(value[:end])
		if strings.TrimSpace(value[end:]) != "" {
			s.provisional = false
			s.previous = nil
			s.phase = argumentStreamInvalid
		}
		return
	}
	if !json.Valid([]byte(value)) {
		s.phase = argumentStreamInvalid
		return
	}
	s.store(value)
}

func (s *streamingToolArguments) store(value string) {
	switch s.key {
	case "path", "description", "command", "workdir", "run_in_background", "timeout_ms", "yield_time_ms":
		if s.values == nil {
			s.values = make(map[string]json.RawMessage)
		}
		s.values[s.key] = json.RawMessage(value)
	}
}

func (s *streamingToolArguments) count() int {
	trimmed := strings.TrimSpace(s.raw)
	if trimmed == "" {
		return 0
	}
	start := len(s.raw) - len(strings.TrimLeftFunc(s.raw, unicode.IsSpace))
	end := start + len(trimmed)
	return s.runes + utf8.RuneCountInString(s.raw[s.runeOffset:]) - utf8.RuneCountInString(s.raw[:start]) - utf8.RuneCountInString(s.raw[end:])
}

func (b *Block) streamingArguments(raw string) *streamingToolArguments {
	if b.streamArgs == nil {
		name := toolNameKey(b.ToolName)
		b.streamArgs = &streamingToolArguments{readFields: name == tools.NameWrite || name == tools.NameEdit || name == tools.NameShell}
	}
	b.streamArgs.update(raw)
	return b.streamArgs
}

func (b *Block) streamedDisplayArgs(raw, result string) string {
	switch toolNameKey(b.ToolName) {
	case tools.NameWrite, tools.NameEdit:
		s := b.streamingArguments(raw)
		value := s.values["path"]
		if value == nil {
			return ""
		}
		return fileToolPathDisplayArgs(streamedFileToolPath(`{"path":` + string(value) + `}`))
	case tools.NameShell:
		s := b.streamingArguments(raw)
		if len(s.values) == 0 {
			return ""
		}
		args, _ := json.Marshal(s.values)
		return string(args)
	default:
		return streamingToolDisplayArgs(b.ToolName, raw, result)
	}
}

// An incomplete edit has no readable replacement preview. Avoid decoding its
// growing payload when rendering a path and received-character progress.
func (b *Block) editArgsIncomplete() bool {
	return b.ToolName == tools.NameEdit && b.streamArgs != nil && b.streamArgs.phase != argumentStreamDone
}
