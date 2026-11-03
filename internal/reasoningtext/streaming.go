package reasoningtext

import "strings"

// StreamingHeadings retains normalized complete lines while the last line is
// still growing. Its output follows NormalizeStreamingHeadings at every append.
// Each instance belongs to one stream; copies must not share its builder.
type StreamingHeadings struct {
	input  string
	output string
	offset int
	prefix strings.Builder
}

// CacheBytes estimates retained normalized text, excluding the caller's input.
func (s *StreamingHeadings) CacheBytes() int {
	if s == nil {
		return 0
	}
	bytes := s.prefix.Cap()
	if s.output != s.input {
		bytes += len(s.output)
	}
	return bytes
}

func (s *StreamingHeadings) Normalize(input string) string {
	if input == s.input {
		return s.output
	}
	if !strings.HasPrefix(input, s.input) {
		*s = StreamingHeadings{}
	}
	tail := input[s.offset:]
	normalized := NormalizeStreamingHeadings(tail)
	// Keep the last complete line as well as the open line. A heading on the
	// open line can still insert a blank before it, including across a single
	// newline. Earlier line boundaries cannot change once both lines end.
	if last := strings.LastIndexByte(tail, '\n'); last > 0 {
		if previous := strings.LastIndexByte(tail[:last], '\n'); previous >= 0 {
			cut := previous + 1
			suffix := NormalizeStreamingHeadings(tail[cut:])
			s.prefix.WriteString(normalized[:len(normalized)-len(suffix)])
			s.offset += cut
			normalized = suffix
		}
	}
	s.input = input
	if normalized == input[s.offset:] && s.prefix.String() == input[:s.offset] {
		s.output = input
	} else {
		s.output = s.prefix.String() + normalized
	}
	return s.output
}
