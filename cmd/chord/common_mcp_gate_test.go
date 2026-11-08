package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/recovery"
)

func TestRuntimeMCPControlCancelledBeforeAdmissionPreservesIntent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		occupied bool
		deadline bool
	}{
		{name: "deadline while occupied", occupied: true, deadline: true},
		{name: "cancel while occupied", occupied: true},
		{name: "cancel with available gate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ac := newTestAppContext(t)
			ac.MCPTargets = testMCPTargets([]mcp.ServerConfig{{Name: "sample", Manual: true}})
			ac.MCPMgr = mcp.NewPendingManagerWithClientInfo(ac.MCPTargets, mcp.ClientInfo{}, nil)
			defer ac.MCPMgr.Close()
			ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
			ac.MCPCatalog.SetDesiredEnabled("sample", true)
			if err := persistMCPEnabledIntent(ac.SessionDir, []string{"sample"}); err != nil {
				t.Fatal(err)
			}
			metaPath := filepath.Join(ac.SessionDir, "session-meta.json")
			before, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := ac.MCPMgr.Snapshot()
			if tc.occupied {
				if err := ac.acquireMCPRuntime(t.Context()); err != nil {
					t.Fatal(err)
				}
				defer ac.releaseMCPRuntime()
			}
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := controlRuntimeMCP(ctx, ac, agent.MCPControlRequest{
					Action: agent.MCPControlDisable, Servers: []string{"sample"},
				})
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("control error = %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled control kept waiting for runtime admission")
			}
			if !ac.MCPCatalog.DesiredEnabled("sample") {
				t.Fatal("cancelled control changed catalog intent")
			}
			if got := ac.MCPMgr.Snapshot(); !reflect.DeepEqual(got, snapshot) {
				t.Fatalf("cancelled control changed connection state: before=%+v after=%+v", snapshot, got)
			}
			after, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("cancelled control changed persisted session intent")
			}
		})
	}
}

func TestMCPRuntimeGateCancelledReadyDoesNotConsumeAdmission(t *testing.T) {
	ac := &AppContext{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 100 {
		if err := ac.acquireMCPRuntime(ctx); !errors.Is(err, context.Canceled) {
			if err == nil {
				ac.releaseMCPRuntime()
			}
			t.Fatalf("cancelled acquisition = %v", err)
		}
	}
	next, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := ac.acquireMCPRuntime(next); err != nil {
		t.Fatalf("cancelled waiter consumed admission: %v", err)
	}
	ac.releaseMCPRuntime()
}

func TestMCPRuntimeWaitersCancelWithoutPublishing(t *testing.T) {
	for _, name := range []string{"reconcile", "idle reload", "session restore"} {
		t.Run(name, func(t *testing.T) {
			ac := newTestAppContext(t)
			ac.MCPTargets = testMCPTargets([]mcp.ServerConfig{{Name: "sample", Manual: true}})
			ac.MCPMgr = mcp.NewPendingManagerWithClientInfo(ac.MCPTargets, mcp.ClientInfo{}, nil)
			defer ac.MCPMgr.Close()
			ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
			if err := ac.acquireMCPRuntime(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer ac.releaseMCPRuntime()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			var staleGeneration agent.MCPReadyGeneration
			var restoreGeneration uint64
			if name == "session restore" {
				staleGeneration, restoreGeneration, _ = beginMCPRestore(ac)
				// A superseding generation must not be released by this waiter.
				beginMCPRestore(ac)
				defer finishMCPRestore(ac, ac.mcpRestoreGen.Load())
			}
			current := ac.MainAgent.CurrentMCPReadyGeneration()
			go func() {
				switch name {
				case "reconcile":
					done <- reconcileRuntimeMCPCredentials(ctx, ac)
				case "idle reload":
					done <- restoreRuntimeMCP(ac, nil)(ctx)
				case "session restore":
					runMCPRestoreFlow(ac, ac.SessionDir, staleGeneration, restoreGeneration, ctx, false)
					done <- ctx.Err()
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("waiter error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled runtime waiter did not return")
			}
			if ac.MainAgent.CurrentMCPReadyGeneration() != current {
				t.Fatal("cancelled waiter replaced the current readiness generation")
			}
		})
	}
}

func TestRuntimeMCPControlReleasesAdmissionAfterSuccessAndFailure(t *testing.T) {
	ac := newTestAppContext(t)
	ac.MCPTargets = testMCPTargets([]mcp.ServerConfig{{Name: "sample", Manual: true}})
	ac.MCPMgr = mcp.NewPendingManagerWithClientInfo(ac.MCPTargets, mcp.ClientInfo{}, nil)
	defer ac.MCPMgr.Close()
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := controlRuntimeMCP(ctx, ac, agent.MCPControlRequest{
		Action: agent.MCPControlEnable, Servers: []string{"sample"},
	})
	if err == nil || len(result.Enabled) != 0 || !ac.MCPCatalog.DesiredEnabled("sample") {
		t.Fatalf("failed connection lost accepted intent: result=%+v err=%v", result, err)
	}
	meta, err := recovery.LoadSessionMeta(ac.SessionDir)
	if err != nil || meta == nil || len(meta.MCPEnabledServers) != 1 || meta.MCPEnabledServers[0] != "sample" {
		t.Fatalf("failed connection lost persisted intent: meta=%+v err=%v", meta, err)
	}
	result, err = controlRuntimeMCP(ctx, ac, agent.MCPControlRequest{
		Action: agent.MCPControlDisable, Servers: []string{"sample", "missing"},
	})
	if err == nil || len(result.Disabled) != 1 || result.Disabled[0] != "sample" {
		t.Fatalf("partial success = %+v, %v", result, err)
	}
	meta, err = recovery.LoadSessionMeta(ac.SessionDir)
	if err != nil || meta != nil && len(meta.MCPEnabledServers) != 0 {
		t.Fatalf("disabled intent not persisted: meta=%+v err=%v", meta, err)
	}
	if err := ac.acquireMCPRuntime(ctx); err != nil {
		t.Fatalf("operation retained admission: %v", err)
	}
	ac.releaseMCPRuntime()
}
