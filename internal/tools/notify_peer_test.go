package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
)

type peerMessengerStub struct {
	request AgentPeerNoticeRequest
}

func (s *peerMessengerStub) NotifyPeerMessage(_ context.Context, request AgentPeerNoticeRequest) (TaskHandle, error) {
	s.request = request
	return TaskHandle{Status: "queued", TaskID: request.TargetTaskID}, nil
}

func TestNotifyPeerValidatesAndRoutesNotice(t *testing.T) {
	messenger := &peerMessengerStub{}
	tool := NewNotifyPeerTool(messenger)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-b","message":"schema is ready","kind":"dependency_update"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, `"task_id":"task-b"`) || messenger.request.Message != "schema is ready" || messenger.request.Kind != "dependency_update" {
		t.Fatalf("result=%q request=%#v", result, messenger.request)
	}
}

func TestNotifyPeerRejectsRequestResponseFields(t *testing.T) {
	tool := NewNotifyPeerTool(&peerMessengerStub{})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-b","message":"answer","message_type":"response","correlation_id":"corr-1"}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("structured peer fields error = %v", err)
	}
}

func TestNotifyPeerUsesIndependentPermission(t *testing.T) {
	tool := NewNotifyPeerTool(&peerMessengerStub{})
	if tool.VisibleWithRuleset(nil) {
		t.Fatal("notify_peer should require an explicit or wildcard permission")
	}
	if tool.VisibleWithRuleset(permission.Ruleset{{Permission: NameNotifyPeer, Pattern: "*", Action: permission.ActionDeny}}) {
		t.Fatal("notify_peer should be hidden when its independent permission is denied")
	}
	if !tool.VisibleWithRuleset(permission.Ruleset{{Permission: NameNotify, Pattern: "*", Action: permission.ActionDeny}, {Permission: NameNotifyPeer, Pattern: "*", Action: permission.ActionAllow}}) {
		t.Fatal("notify_peer should not inherit notify denial")
	}
}
