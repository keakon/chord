package recovery

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func readAllMessageLog(t *testing.T, input string) ([]string, error) {
	t.Helper()
	reader := newMessageLogReader(strings.NewReader(input))
	var contents []string
	for range 16 {
		var msg message.Message
		if err := reader.next(&msg); err != nil {
			return contents, err
		}
		contents = append(contents, msg.Content)
	}
	t.Fatal("reader did not terminate")
	return nil, nil
}

func TestMessageLogReaderClassifiesErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
		check func(error) bool
	}{
		{"clean", "{\"content\":\"a\"}\n\n{\"content\":\"b\"}\n", []string{"a", "b"}, func(err error) bool { return err == io.EOF }},
		{"unterminated complete tail", "{\"content\":\"a\"}\n{\"content\":\"b\"}", []string{"a", "b"}, func(err error) bool { return err == io.EOF }},
		{"truncated tail", "{\"content\":\"a\"}\n{\"content\":\"b", []string{"a"}, func(err error) bool { return errors.Is(err, errTruncatedRecord) }},
		{"closing brace line", "{\"content\":\"a\"}\n}\n{\"content\":\"b\"}\n", []string{"a"}, func(err error) bool { return err != nil && err != io.EOF && !errors.Is(err, errTruncatedRecord) }},
		{"mid-file garbage", "{\"content\":\"a\"}\nnot json\n{\"content\":\"b\"}\n", []string{"a"}, func(err error) bool { return err != nil && err != io.EOF && !errors.Is(err, errTruncatedRecord) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readAllMessageLog(t, tt.input)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("contents = %v, want %v", got, tt.want)
			}
			if !tt.check(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
