package message

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticSourceIsRequestOnly(t *testing.T) {
	m := Message{Role: RoleUser, Content: "sample", DiagnosticSource: DiagnosticSourceInstructions}
	cloned := m.Clone()
	if cloned.DiagnosticSource != m.DiagnosticSource {
		t.Fatal("request clone lost source")
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "DiagnosticSource") || strings.Contains(string(data), DiagnosticSourceInstructions) {
		t.Fatal("source entered persisted message", string(data))
	}
	var restored Message
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.DiagnosticSource != "" {
		t.Fatal("source restored from history")
	}
}
