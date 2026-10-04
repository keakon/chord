package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClientListToolsBudgets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		page  func(int) json.RawMessage
		want  string
		calls int
	}{
		{"pages", func(n int) json.RawMessage { return json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":"%d"}`, n)) }, "pages", maxToolListPages},
		{"tools", func(n int) json.RawMessage {
			defs := make([]MCPToolDef, maxListedTools/2+1)
			for i := range defs {
				defs[i].Name = fmt.Sprintf("tool_%d_%d", n, i)
			}
			raw, err := json.Marshal(toolsListResult{Tools: defs, NextCursor: new(fmt.Sprint(n))})
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}, "tools", 2},
		{"bytes", func(n int) json.RawMessage {
			return json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":"%d","padding":"%s"}`, n, strings.Repeat("a", maxToolListBytes/4-100)))
		}, "response bytes", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			tr := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
			tr.send = func(context.Context, JSONRPCRequest) (JSONRPCResponse, error) {
				calls++
				if calls > tc.calls {
					t.Fatal("budget was not enforced")
				}
				return JSONRPCResponse{Result: tc.page(calls)}, nil
			}
			defs, err := NewClientWithInfo("sample", tr, testClientInfo).ListTools(t.Context())
			if defs != nil || err == nil || !strings.Contains(err.Error(), tc.want) || calls != tc.calls {
				t.Fatalf("defs=%v calls=%d err=%v", defs, calls, err)
			}
		})
	}
}

func TestClientListToolsCancellationAndPageErrors(t *testing.T) {
	for _, scenario := range []string{"before send", "after first", "after final", "during send", "rpc error", "send error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			pageErr := errors.New("page failed")
			rpcErr := &JSONRPCError{Code: -32000, Message: "page failed"}
			wantErr := error(context.Canceled)
			if scenario == "before send" {
				cancel()
			}
			tr := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
			tr.send = func(callCtx context.Context, _ JSONRPCRequest) (JSONRPCResponse, error) {
				calls++
				if callCtx != ctx {
					t.Fatal("context changed")
				}
				if calls > 2 {
					t.Fatal("unexpected extra request")
				}
				result := `{"tools":[{"name":"first"}],"nextCursor":"a"}`
				if calls == 2 {
					result = `{"tools":[{"name":"last"}]}`
				}
				switch scenario {
				case "after first":
					cancel()
				case "after final":
					if calls == 2 {
						cancel()
					}
				case "during send":
					if calls == 2 {
						cancel()
						return JSONRPCResponse{}, callCtx.Err()
					}
				case "rpc error":
					if calls == 2 {
						wantErr = rpcErr
						return JSONRPCResponse{Error: rpcErr}, nil
					}
				case "send error":
					if calls == 2 {
						wantErr = pageErr
						return JSONRPCResponse{}, pageErr
					}
				}
				return JSONRPCResponse{Result: json.RawMessage(result)}, nil
			}
			defs, err := NewClientWithInfo("sample", tr, testClientInfo).ListTools(ctx)
			if defs != nil || !errors.Is(err, wantErr) {
				t.Fatalf("defs=%v err=%v want=%v", defs, err, wantErr)
			}
			if scenario == "before send" && calls != 0 {
				t.Fatalf("cancelled request sent: %d", calls)
			}
		})
	}
}

func TestClientListToolsAcceptsFinalPageAtLimit(t *testing.T) {
	calls := 0
	tr := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
	tr.send = func(context.Context, JSONRPCRequest) (JSONRPCResponse, error) {
		calls++
		if calls == maxToolListPages {
			return JSONRPCResponse{Result: json.RawMessage(`{"tools":[]}`)}, nil
		}
		return JSONRPCResponse{Result: json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":"%d"}`, calls))}, nil
	}
	defs, err := NewClientWithInfo("sample", tr, testClientInfo).ListTools(t.Context())
	if err != nil || len(defs) != 0 || calls != maxToolListPages {
		t.Fatalf("defs=%v calls=%d err=%v", defs, calls, err)
	}
}
