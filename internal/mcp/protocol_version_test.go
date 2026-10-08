package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInitializeRejectsUnsupportedProtocolBeforeReady(t *testing.T) {
	for _, version := range []string{"", "2024-11-05", "2099-01-01"} {
		t.Run(version, func(t *testing.T) {
			transport := newFakeTransport()
			transport.onMethod("initialize", initializeResult{ProtocolVersion: version})
			client := NewClientWithInfo("sample", transport, testClientInfo)
			if err := client.Initialize(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
				t.Fatalf("Initialize = %v", err)
			}
			if transport.notifCount() != 0 {
				t.Fatal("unsupported version sent initialized notification")
			}
		})
	}
}

func TestHTTPInitializeBindsNegotiatedProtocolHeaders(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		methods = append(methods, req.Method)
		want := protocolVersion
		if req.Method == "initialize" {
			want = ""
			params := req.Params.(map[string]any)
			if params["protocolVersion"] != protocolVersion {
				t.Errorf("protocolVersion = %v", params["protocolVersion"])
			}
		}
		if got := r.Header.Get(mcpProtocolVersionHeader); got != want {
			t.Errorf("%s header = %q, want %q", req.Method, got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"protocolVersion":"2025-06-18"}`)})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"tools":[]}`)})
		}
	}))
	defer server.Close()
	transport := NewHTTPTransport(server.URL, map[string]string{mcpProtocolVersionHeader: "stale"})
	client := NewClientWithInfo("sample", transport, testClientInfo)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestInitializeRequiresReadyNotification(t *testing.T) {
	transport := newFakeTransport()
	transport.onMethod("initialize", initializeResult{ProtocolVersion: protocolVersion})
	failure := errors.New("notification unavailable")
	transport.notifyErrs = []error{failure}
	client := NewClientWithInfo("sample", transport, testClientInfo)
	if err := client.Initialize(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("Initialize = %v", err)
	}
}
