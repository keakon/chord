package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestExportOmitsInputDeliveryIdentity(t *testing.T) {
	exported, err := Export([]message.Message{{Role: message.RoleUser, Content: "sample", InputID: "input-1", InputRequestID: "request-1"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "input_id") || strings.Contains(string(data), "input_request_id") {
		t.Fatal("export leaked queue identity")
	}
}
