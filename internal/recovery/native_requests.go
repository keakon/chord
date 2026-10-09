package recovery

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
)

// NativeRequestJournal records authorization before a provider can execute a
// server tool. A missing receipt is outcome_unknown, including the unavoidable
// crash window between the durable authorization and network dispatch.
type NativeRequestJournal struct {
	SessionDir, AgentID string
	TurnID, Generation  uint64
}

func (j NativeRequestJournal) directory() string {
	return filepath.Join(j.SessionDir, "native-requests")
}

func (j NativeRequestJournal) Begin(request any, continuations []string) (string, error) {
	if j.SessionDir == "" || j.AgentID == "" {
		return "", fmt.Errorf("native request journal requires a durable session and agent identity")
	}
	dir := j.directory()
	if err := privatefs.EnsureDir(j.SessionDir, dir); err != nil {
		return "", fmt.Errorf("create native request journal: %w", err)
	}
	records, err := j.pendingRecords()
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if slices.Contains(continuations, record.ID) {
			if err := j.completed(record.ID); err != nil {
				return "", err
			}
			continue
		}
		return "", unreconciledNativeRequest(record.ID)
	}
	id := rand.Text()
	record := struct {
		ID         string `json:"id"`
		AgentID    string `json:"agent_id"`
		TurnID     uint64 `json:"turn_id"`
		Generation uint64 `json:"generation"`
		Replay     string `json:"replay"`
		Request    any    `json:"request"`
	}{id, j.AgentID, j.TurnID, j.Generation, "never_automatic", request}
	if err := j.write(id+".request.json", record); err != nil {
		return "", err
	}
	return id, nil
}

type nativeRequestResult struct {
	Outcome  message.NativeRequestOutcome `json:"outcome"`
	Error    string                       `json:"error,omitempty"`
	Response *message.Response            `json:"response,omitempty"`
}

func (j NativeRequestJournal) Finish(id string, outcome message.NativeRequestOutcome, response *message.Response, callErr error) error {
	switch outcome {
	case message.NativeRequestCompleted:
		if callErr != nil || response == nil {
			return fmt.Errorf("completed native request requires a successful response")
		}
	case message.NativeRequestNotSent, message.NativeRequestRejected, message.NativeRequestUnknown:
		if callErr == nil {
			return fmt.Errorf("failed native request requires an error")
		}
	default:
		return fmt.Errorf("invalid native request outcome %q", outcome)
	}
	detail := ""
	if callErr != nil {
		detail = callErr.Error()
	}
	return j.write(id+".result.json", nativeRequestResult{outcome, detail, response})
}

// Acknowledge runs only after the assistant receipt is durable in canonical
// history. It lets future compacted requests omit old native blocks safely.
func (j NativeRequestJournal) Acknowledge(native *message.NativeToolHistory) error {
	if native == nil {
		return nil
	}
	for _, id := range native.RequestIDs {
		if err := j.completed(id); err != nil {
			return err
		}
		if err := j.write(id+".ack", struct {
			Recorded bool `json:"recorded"`
		}{true}); err != nil {
			return err
		}
	}
	return nil
}
func (j NativeRequestJournal) write(name string, value any) error {
	if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid native receipt name")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode native receipt: %w", err)
	}
	dir := j.directory()
	if err := privatefs.WriteFileSynced(j.SessionDir, filepath.Join(dir, name), raw); err != nil {
		return fmt.Errorf("persist native receipt: %w", err)
	}
	if err := privatefs.SyncDir(dir); err != nil {
		return fmt.Errorf("sync native journal: %w", err)
	}
	return nil
}

// completed validates a result before a continuation or durable acknowledgement
// can release its replay barrier. Request IDs from message history alone are
// never evidence that the result was durably recorded.
func (j NativeRequestJournal) completed(id string) error {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("invalid native request ID")
	}
	raw, err := os.ReadFile(filepath.Join(j.directory(), id+".result.json"))
	if err != nil {
		return fmt.Errorf("read native result: %w", err)
	}
	var result nativeRequestResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode native result: %w", err)
	}
	if result.Outcome != message.NativeRequestCompleted || result.Response == nil {
		return fmt.Errorf("native request %s has outcome_unknown", id)
	}
	return nil
}
