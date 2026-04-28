package mcp

import "testing"

func TestRegisteredMCPToolName(t *testing.T) {
	tests := []struct {
		server, remote, want string
	}{
		{"remote", "alpha_tool", "mcp_remote_alpha_tool"},
		{"my-server", "foo", "mcp_my_server_foo"},
		{"Test", "Bar", "mcp_test_bar"},
	}
	for _, tt := range tests {
		if g := RegisteredMCPToolName(tt.server, tt.remote); g != tt.want {
			t.Errorf("RegisteredMCPToolName(%q,%q) = %q, want %q", tt.server, tt.remote, g, tt.want)
		}
	}
}
