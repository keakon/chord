package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/permission"
)

func TestMCPStatusProjectionIsImmutableAndRoleBound(t *testing.T) {
	a := &MainAgent{agentConfigs: map[string]*config.AgentConfig{"builder": {Name: "builder"}, "explorer": {Name: "explorer"}}}
	raw := mcp.RuntimeSnapshot{SchemaVersion: 1, BindingID: "manager", Version: 1, Servers: []mcp.ServerView{{Ref: mcp.ServerRef{WorkspaceID: "workspace", Owner: mcp.MainOwner, ServerName: "sample"}, ScopeRequirements: []mcp.ScopeRequirementView{{Scopes: []string{"read"}}}}}}
	a.SetMCPStatusSnapshotFunc(func() mcp.RuntimeSnapshot { return raw.Clone() })
	first := a.MCPRuntimeSnapshot()
	if first.BindingID == "" || first.BindingID == "manager" || first.Version != 1 {
		t.Fatalf("view identity = %+v", first)
	}
	first.Servers[0].ScopeRequirements[0].Scopes[0] = "changed"
	if a.MCPRuntimeSnapshot().Servers[0].ScopeRequirements[0].Scopes[0] != "read" {
		t.Fatal("cached status aliases consumer")
	}
	a.RefreshMCPStatusSnapshot()
	if a.MCPRuntimeSnapshot().Version != 1 {
		t.Fatal("identical snapshot changed version")
	}
	raw.Servers[0].Auth = mcp.AuthReady
	a.RefreshMCPStatusSnapshot()
	if a.MCPRuntimeSnapshot().Version != 2 {
		t.Fatal("status change did not advance version")
	}
	a.stateMu.Lock()
	a.activeConfig = a.agentConfigs["explorer"]
	a.stateMu.Unlock()
	a.RefreshMCPStatusSnapshot()
	if a.MCPRuntimeSnapshot().BindingID == first.BindingID {
		t.Fatal("role switch retained old view binding")
	}
	if _, err := a.MCPRuntimeSnapshotForAgent("missing"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
func TestMCPStatusProjectionFiltersPrivateOwnersAndPermissions(t *testing.T) {
	all := []mcp.ServerView{{Ref: mcp.ServerRef{Owner: mcp.MainOwner, ServerName: "shared"}}, {Ref: mcp.ServerRef{Owner: "agent:explorer", ServerName: "private"}}, {Ref: mcp.ServerRef{Owner: "agent:reviewer", ServerName: "other"}}}
	rules := permission.Ruleset{{Permission: "mcp_shared_*", Pattern: "*", Action: permission.ActionDeny}}
	visible := filterMCPStatusServers(all, "explorer", rules)
	if len(visible) != 1 || visible[0].Ref.ServerName != "private" {
		t.Fatalf("visibility = %+v", visible)
	}
}

func TestMCPStatusProjectionFiltersHiddenAliases(t *testing.T) {
	all := []mcp.ServerView{
		{Ref: mcp.ServerRef{Owner: mcp.MainOwner, ServerName: "visible"}, Aliases: []string{"visible", "hidden"}},
		{Ref: mcp.ServerRef{Owner: mcp.MainOwner, ServerName: "hidden"}, Aliases: []string{"visible", "hidden"}},
	}
	rules := permission.Ruleset{{Permission: "mcp_hidden_*", Pattern: "*", Action: permission.ActionDeny}}
	visible := filterMCPStatusServers(all, "builder", rules)
	if len(visible) != 1 || len(visible[0].Aliases) != 1 || visible[0].Aliases[0] != "visible" {
		t.Fatalf("alias visibility = %+v", visible)
	}
	if len(all[0].Aliases) != 2 {
		t.Fatal("filter mutated source aliases")
	}
}

func TestMCPStatusReadersDoNotWaitForOutputBackpressure(t *testing.T) {
	sampled := make(chan struct{})
	a := &MainAgent{outputCh: make(chan AgentEvent, 1), stoppingCh: make(chan struct{})}
	t.Cleanup(func() { close(a.stoppingCh) })
	a.outputCh <- LoopStateChangedEvent{}
	a.mcpStatus.provider = func() mcp.RuntimeSnapshot {
		close(sampled)
		return mcp.RuntimeSnapshot{SchemaVersion: 1, Servers: []mcp.ServerView{}}
	}
	finished := make(chan struct{})
	go func() { defer close(finished); a.RefreshMCPStatusSnapshot() }()
	<-sampled
	read := make(chan mcp.RuntimeSnapshot, 1)
	go func() { read <- a.MCPRuntimeSnapshot() }()
	select {
	case snapshot := <-read:
		if snapshot.BindingID == "" {
			t.Fatal("reader observed uninitialized projection")
		}
	case <-time.After(time.Second):
		t.Fatal("status reader waited for a full output channel")
	}
	<-a.outputCh
	<-finished
}
