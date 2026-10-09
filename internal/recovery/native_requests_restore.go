package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

type nativeJournalRecord struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	Request struct {
		Target        string                          `json:"target"`
		Protocol      string                          `json:"protocol"`
		APIURL        string                          `json:"api_url"`
		Authorization message.NativeToolAuthorization `json:"authorization"`
	} `json:"request"`
}

func unreconciledNativeRequest(id string) error {
	return fmt.Errorf("native request %s has an unreconciled result or outcome_unknown; inspect the session native-requests receipts before starting a new session", id)
}

func (j NativeRequestJournal) pendingRecords() ([]nativeJournalRecord, error) {
	if j.SessionDir == "" || j.AgentID == "" {
		return nil, fmt.Errorf("native request journal requires a durable session and agent identity")
	}
	entries, err := os.ReadDir(j.directory())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read native journal: %w", err)
	}
	var records []nativeJournalRecord
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".request.json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".request.json")
		// Confirmed requests need no payload decoding on the request path.
		if _, err := os.Stat(filepath.Join(j.directory(), id+".ack")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read native acknowledgement: %w", err)
		}
		raw, err := os.ReadFile(filepath.Join(j.directory(), entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read native authorization: %w", err)
		}
		var record nativeJournalRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("decode native authorization: %w", err)
		}
		if record.AgentID == j.AgentID {
			if record.ID != id {
				return nil, fmt.Errorf("native authorization ID does not match its file")
			}
			// A durable pre-execution failure needs no assistant receipt: no
			// model decision or server execution took place. A missing or
			// unreadable result still retains the barrier.
			resultRaw, readErr := os.ReadFile(filepath.Join(j.directory(), id+".result.json"))
			var result nativeRequestResult
			if readErr == nil && json.Unmarshal(resultRaw, &result) == nil && result.Outcome.Unexecuted() && result.Error != "" {
				continue
			}
			records = append(records, record)
		}
	}
	return records, nil
}

func (r nativeJournalRecord) unknownReceipt() *message.NativeToolHistory {
	return &message.NativeToolHistory{
		OutcomeUnknown: true, RequestIDs: []string{r.ID},
		Target: r.Request.Target, Protocol: r.Request.Protocol,
		APIURL: r.Request.APIURL, Authorization: r.Request.Authorization,
	}
}

// Check gates every agent request, including requests without native tools.
// Configuration changes cannot release an outstanding execution barrier.
func (j NativeRequestJournal) Check() (*message.NativeToolHistory, error) {
	records, err := j.pendingRecords()
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return records[0].unknownReceipt(), unreconciledNativeRequest(records[0].ID)
}

// Restore reconciles journal records with durable canonical history. A completed result
// releases the barrier only when its receipt is already in canonical history.
// Otherwise an unknown receipt preserves available observations without
// inventing an assistant decision or replaying a provider/local tool request.
func (j NativeRequestJournal) Restore(history []message.Message, persist func(message.Message) error) ([]message.Message, error) {
	records, err := j.pendingRecords()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		var recorded *message.NativeToolHistory
		for _, msg := range history {
			if msg.NativeTools != nil && slices.Contains(msg.NativeTools.RequestIDs, record.ID) {
				recorded = msg.NativeTools
				if recorded.OutcomeUnknown {
					break
				}
			}
		}
		if recorded != nil {
			if recorded.OutcomeUnknown {
				continue
			}
			if j.completed(record.ID) == nil {
				if err := j.Acknowledge(&message.NativeToolHistory{RequestIDs: []string{record.ID}}); err != nil {
					return nil, err
				}
				continue
			}
		}
		msg := message.Message{Role: message.RoleAssistant, NativeTools: record.unknownReceipt()}
		raw, readErr := os.ReadFile(filepath.Join(j.directory(), record.ID+".result.json"))
		var result nativeRequestResult
		if readErr == nil && json.Unmarshal(raw, &result) == nil && result.Response != nil && result.Response.NativeTools != nil {
			resp := result.Response
			observation := resp.NativeTools.Clone()
			msg.NativeTools.Items = observation.Items
			msg.NativeTools.Calls = observation.Calls
			msg.NativeTools.Container = observation.Container
		}
		if persist == nil {
			return nil, fmt.Errorf("durable native recovery persistence is unavailable")
		}
		if err := persist(msg); err != nil {
			return nil, fmt.Errorf("persist recovered native receipt: %w", err)
		}
		history = append(slices.Clone(history), msg)
	}
	return history, nil
}
