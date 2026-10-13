// Package inputhistory stores human-submitted composer text independently of sessions.
package inputhistory

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Kind string
type PartKind string

const (
	Prompt         Kind     = "prompt"
	SlashCommand   Kind     = "slash_command"
	ShellCommand   Kind     = "shell_command"
	Text           PartKind = "text"
	Paste          PartKind = "paste"
	MaxRecordBytes          = 4 << 20
	MaxParts                = 4096
)

var ErrTooLarge = errors.New("input history record too large")

type TextPart struct {
	Kind PartKind `json:"kind"`
	Text string   `json:"text"`
}
type Record struct {
	ID              string     `json:"id"`
	SubmittedAt     time.Time  `json:"submitted_at"`
	ProjectKey      string     `json:"project_key"`
	ProjectLabel    string     `json:"project_label"`
	SessionID       string     `json:"session_id"`
	Kind            Kind       `json:"kind"`
	TextParts       []TextPart `json:"text_parts"`
	AttachmentCount int        `json:"attachment_count"`
}
type Options struct {
	Enabled    bool
	MaxEntries int
	MaxBytes   int64
}

func DefaultOptions() Options { return Options{true, 10000, 32 << 20} }
func (o Options) Validate() error {
	if o.MaxEntries < 1 || o.MaxEntries > 100000 || o.MaxBytes < 1 || o.MaxBytes > 256<<20 {
		return fmt.Errorf("input_history: max_entries must be 1..100000 and max_bytes 1..268435456")
	}
	return nil
}
func (r Record) Validate() error {
	if r.ID == "" || r.ProjectKey == "" || r.AttachmentCount < 0 {
		return errors.New("invalid input history identity or attachment count")
	}
	if r.Kind != Prompt && r.Kind != SlashCommand && r.Kind != ShellCommand {
		return errors.New("invalid input history kind")
	}
	if len(r.TextParts) > MaxParts {
		return ErrTooLarge
	}
	for _, s := range []string{r.ID, r.ProjectKey, r.ProjectLabel, r.SessionID} {
		if !utf8.ValidString(s) {
			return errors.New("invalid input history UTF-8")
		}
	}
	nonempty := false
	for _, p := range r.TextParts {
		if (p.Kind != Text && p.Kind != Paste) || !utf8.ValidString(p.Text) {
			return errors.New("invalid input history part")
		}
		nonempty = nonempty || strings.TrimSpace(p.Text) != ""
	}
	if !nonempty {
		return errors.New("empty input history text")
	}
	return nil
}
func (r Record) Size() int64 {
	n := 256 + len(r.ID) + len(r.ProjectKey) + len(r.ProjectLabel) + len(r.SessionID)
	for _, p := range r.TextParts {
		n += 64 + len(p.Text)
	}
	return int64(n)
}
