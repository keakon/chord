package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/notifymcp"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
)

func testExternalNotifyWorker() *externalWorker {
	return &externalWorker{
		ctx:          context.Background(),
		instanceID:   "agent-1",
		taskID:       "task-1",
		ownerAgentID: "main",
		ownerTaskID:  "main-task",
	}
}

func TestExternalNotifyMailboxIsReportOnlyNotice(t *testing.T) {
	worker := testExternalNotifyWorker()
	mailbox := worker.externalNotifyMailbox(notifymcp.Request{
		Message: "Found the failing test",
		Kind:    notifymcp.KindNotice,
		Payload: json.RawMessage(`{"test":"sample"}`),
	})
	if mailbox.Kind != SubAgentMailboxKindNotice || mailbox.MessageType != AgentMessageTypeNotice {
		t.Fatalf("mailbox kind/type = %q/%q", mailbox.Kind, mailbox.MessageType)
	}
	if mailbox.Priority != SubAgentMailboxPriorityNotify || mailbox.Subtype != "" {
		t.Fatalf("mailbox priority/subtype = %q/%q", mailbox.Priority, mailbox.Subtype)
	}
	if !mailbox.ReportOnly || mailbox.RequiresAck {
		t.Fatalf("mailbox report_only=%v requires_ack=%v", mailbox.ReportOnly, mailbox.RequiresAck)
	}
	if mailbox.AgentID != "agent-1" || mailbox.TaskID != "task-1" ||
		mailbox.OwnerAgentID != "main" || mailbox.OwnerTaskID != "main-task" {
		t.Fatalf("mailbox identity = %+v", mailbox)
	}
	if mailbox.Summary != "Found the failing test" || mailbox.Payload != mailbox.Summary {
		t.Fatalf("mailbox summary/payload = %q/%q", mailbox.Summary, mailbox.Payload)
	}
	if string(mailbox.MessagePayload) != `{"test":"sample"}` {
		t.Fatalf("mailbox message payload = %s", mailbox.MessagePayload)
	}
}

func TestExternalNotifyMailboxDecisionInterrupts(t *testing.T) {
	mailbox := testExternalNotifyWorker().externalNotifyMailbox(notifymcp.Request{
		Message: "Choose a migration path",
		Kind:    notifymcp.KindDecision,
	})
	if mailbox.Priority != SubAgentMailboxPriorityInterrupt || mailbox.Subtype != agentMessageSubtypeDecision {
		t.Fatalf("mailbox priority/subtype = %q/%q", mailbox.Priority, mailbox.Subtype)
	}
	if !mailbox.ReportOnly {
		t.Fatal("a decision notice must stay report-only")
	}
}

func TestExternalNotifyMCPRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "empty message", arguments: `{"message":" "}`, want: "message must not be empty"},
		{name: "bad kind", arguments: `{"message":"x","kind":"urgent"}`, want: "kind must be notice or decision"},
		{name: "array payload", arguments: `{"message":"x","payload":[1]}`, want: "payload must be a JSON object"},
		{name: "broken payload", arguments: `{"message":"x","payload":{"a":}}`, want: "parse error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notify","arguments":` + tc.arguments + `}}`)
			reply := testExternalNotifyWorker().handleExternalMCP(context.Background(), notifymcp.ServerName, call)
			if !strings.Contains(string(reply), tc.want) {
				t.Fatalf("reply = %s, want %q", reply, tc.want)
			}
		})
	}
}

func TestExternalNotifyDeliveryRejectsSettledWorker(t *testing.T) {
	for name, prepare := range map[string]func(*externalWorker){
		"finished": func(w *externalWorker) { w.finished.Store(true) },
		"closing":  func(w *externalWorker) { w.closing.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			worker := testExternalNotifyWorker()
			prepare(worker)
			err := worker.deliverExternalNotify(context.Background(), notifymcp.Request{Message: "late"})
			if err == nil || !strings.Contains(err.Error(), "no longer running") {
				t.Fatalf("error = %v, want terminal rejection", err)
			}
		})
	}
}

func TestExternalNotifyMCPServesCatalogAndReportsUnavailableWorker(t *testing.T) {
	worker := testExternalNotifyWorker()
	catalog := worker.handleExternalMCP(context.Background(), notifymcp.ServerName,
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(catalog, &listed); err != nil {
		t.Fatalf("decode tools/list = %s: %v", catalog, err)
	}
	if len(listed.Result.Tools) != 1 || listed.Result.Tools[0].Name != notifymcp.ToolName {
		t.Fatalf("tools/list = %+v, want notify", listed.Result.Tools)
	}

	call := worker.handleExternalMCP(context.Background(), notifymcp.ServerName,
		json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"notify","arguments":{"message":"hello"}}}`))
	var delivered struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(call, &delivered); err != nil {
		t.Fatalf("decode tools/call = %s: %v", call, err)
	}
	if !delivered.Result.IsError || len(delivered.Result.Content) != 1 ||
		!strings.Contains(delivered.Result.Content[0].Text, "unavailable") {
		t.Fatalf("tools/call without an owner = %+v, want a tool error", delivered.Result)
	}
}

func TestExternalNotifyMCPRejectsUnknownOrDisabledServer(t *testing.T) {
	worker := testExternalNotifyWorker()
	for name, call := range map[string]func() json.RawMessage{
		"unknown server": func() json.RawMessage {
			return worker.handleExternalMCP(context.Background(), "other",
				json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		},
		"role disabled": func() json.RawMessage {
			worker.setRuleset(permission.Ruleset{{
				Permission: toolname.Notify,
				Pattern:    "*",
				Action:     permission.ActionDeny,
			}})
			return worker.handleExternalMCP(context.Background(), notifymcp.ServerName,
				json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := call()
			var decoded struct {
				ID    json.RawMessage `json:"id"`
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode MCP rejection = %s: %v", raw, err)
			}
			if decoded.Error.Code != -32601 {
				t.Fatalf("MCP rejection = %s, want -32601", raw)
			}
			if string(decoded.ID) != "1" {
				t.Fatalf("MCP rejection = %s, want the request id echoed instead of null", raw)
			}
		})
	}
}

func TestExternalNotifyEnabledFollowsRuleset(t *testing.T) {
	worker := &externalWorker{}
	if !worker.notifyEnabled() {
		t.Fatal("notify must be visible with an empty ruleset")
	}
	worker.setRuleset(permission.Ruleset{{
		Permission: toolname.Notify,
		Pattern:    "*",
		Action:     permission.ActionDeny,
	}})
	if worker.notifyEnabled() {
		t.Fatal("a role that denies notify must not get the MCP surface")
	}
}
