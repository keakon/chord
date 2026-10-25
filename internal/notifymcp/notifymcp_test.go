package notifymcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func response(t *testing.T, message string, deliver DeliverFunc) map[string]json.RawMessage {
	t.Helper()
	raw := Handle(context.Background(), json.RawMessage(message), deliver)
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Handle(%s) returned invalid JSON %s: %v", message, raw, err)
	}
	return decoded
}

func result(t *testing.T, decoded map[string]json.RawMessage, target any) {
	t.Helper()
	raw, ok := decoded["result"]
	if !ok {
		t.Fatalf("response has no result: %v", decoded)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
}

func TestHandleHandshakeAndCatalog(t *testing.T) {
	initialize := response(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil)
	var initialized struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
	}
	result(t, initialize, &initialized)
	if initialized.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocolVersion = %q, want client version", initialized.ProtocolVersion)
	}
	if _, ok := initialized.Capabilities["tools"]; !ok {
		t.Fatalf("initialize result missing tools capability: %v", initialized)
	}

	notification := response(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if _, ok := notification["id"]; ok {
		t.Fatalf("notification response must not carry an id: %v", notification)
	}
	var empty map[string]any
	result(t, notification, &empty)

	catalog := response(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	var tools struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	result(t, catalog, &tools)
	if len(tools.Tools) != 1 || tools.Tools[0].Name != ToolName {
		t.Fatalf("tools/list = %+v, want one notify tool", tools.Tools)
	}
	properties, _ := tools.Tools[0].InputSchema["properties"].(map[string]any)
	if _, ok := properties["message"]; !ok {
		t.Fatalf("notify schema = %+v, want message property", tools.Tools[0].InputSchema)
	}

	ping := response(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`, nil)
	var pingResult map[string]any
	result(t, ping, &pingResult)
	if len(pingResult) != 0 {
		t.Fatalf("ping result = %v, want empty object", pingResult)
	}

	unknown := response(t, `{"jsonrpc":"2.0","id":4,"method":"unknown/method"}`, nil)
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(unknown["error"], &rpcErr); err != nil {
		t.Fatalf("decode unknown-method error: %v", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("unknown method error code = %d, want -32601", rpcErr.Code)
	}
}

func TestHandleForwardsNotify(t *testing.T) {
	requests := make(chan Request, 1)
	deliver := func(_ context.Context, request Request) error {
		requests <- request
		return nil
	}
	decoded := response(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"notify","arguments":{"message":"Need a decision","kind":"decision","payload":{"item":3}}}}`, deliver)
	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	result(t, decoded, &callResult)
	if callResult.IsError || len(callResult.Content) != 1 || callResult.Content[0].Text == "" {
		t.Fatalf("tool result = %+v, want delivered content", callResult)
	}
	select {
	case request := <-requests:
		if request.Kind != "decision" || request.Message != "Need a decision" {
			t.Fatalf("forwarded request = %+v", request)
		}
		if got := string(bytes.TrimSpace(request.Payload)); got != `{"item":3}` {
			t.Fatalf("forwarded payload = %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forwarded notify")
	}
}

func TestHandleNormalizesNotifyBeforeDelivery(t *testing.T) {
	for _, arguments := range []string{
		`{"message":"  Sample notice  ","payload":null}`,
		`{"message":"  Sample notice  ","kind":" notice ","payload":null}`,
	} {
		delivered := false
		response(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notify","arguments":`+arguments+`}}`,
			func(_ context.Context, request Request) error {
				delivered = true
				if request.Message != "Sample notice" || request.Kind != KindNotice || request.Payload != nil {
					t.Fatalf("notification not normalized: %+v", request)
				}
				return nil
			})
		if !delivered {
			t.Fatal("normalized notification not delivered")
		}
	}
}

func TestHandleReportsDeliveryFailureAsToolError(t *testing.T) {
	deliver := func(context.Context, Request) error {
		return context.DeadlineExceeded
	}
	decoded := response(t, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"notify","arguments":{"message":"status"}}}`, deliver)
	var callResult struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	result(t, decoded, &callResult)
	if !callResult.IsError || len(callResult.Content) != 1 || !strings.Contains(callResult.Content[0].Text, "deadline") {
		t.Fatalf("tool result = %+v, want delivery failure as tool error", callResult)
	}
}

func TestHandleRejectsInvalidNotifyArguments(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "empty message", arguments: `{"message":"  "}`, want: "message must not be empty"},
		{name: "bad kind", arguments: `{"message":"x","kind":"urgent"}`, want: "kind must be notice or decision"},
		{name: "array payload", arguments: `{"message":"x","payload":[1]}`, want: "payload must be a JSON object"},
		{name: "string payload", arguments: `{"message":"x","payload":"text"}`, want: "payload must be a JSON object"},
		{name: "oversized message", arguments: `{"message":"` + strings.Repeat("x", MaxMessageBytes+1) + `"}`, want: "message exceeds"},
		{name: "oversized payload", arguments: `{"message":"x","payload":{"text":"` + strings.Repeat("x", MaxMessageBytes) + `"}}`, want: "payload must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notify","arguments":` + tc.arguments + `}}`
			decoded := response(t, message, func(context.Context, Request) error {
				t.Fatal("invalid notification reached delivery")
				return nil
			})
			var callResult struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			result(t, decoded, &callResult)
			if !callResult.IsError {
				t.Fatalf("result = %+v, want tool error", callResult)
			}
			text := ""
			if len(callResult.Content) == 1 {
				text = callResult.Content[0].Text
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("tool error = %q, want %q", text, tc.want)
			}
		})
	}
}

func TestHandleRejectsUnknownTool(t *testing.T) {
	decoded := response(t, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"other","arguments":{}}}`, nil)
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(decoded["error"], &rpcErr); err != nil {
		t.Fatalf("decode unknown-tool error: %v", err)
	}
	if rpcErr.Code != -32602 {
		t.Fatalf("unknown tool error code = %d, want -32602", rpcErr.Code)
	}
}

func TestHandleParseErrorKeepsProtocolAlive(t *testing.T) {
	decoded := response(t, `not json`, nil)
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(decoded["error"], &rpcErr); err != nil {
		t.Fatalf("decode parse error: %v", err)
	}
	if rpcErr.Code != -32700 {
		t.Fatalf("parse error code = %d, want -32700", rpcErr.Code)
	}
	if string(decoded["id"]) != "null" {
		t.Fatalf("parse error id = %s, want null", decoded["id"])
	}
}
