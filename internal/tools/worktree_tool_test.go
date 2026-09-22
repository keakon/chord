package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubWorktreeHost records the requests the tools forward and returns canned
// results, so the tool layer can be exercised without a git repository.
type stubWorktreeHost struct {
	enterReq WorktreeEnterRequest
	enterRes WorktreeEnterResult
	enterErr error

	exitReq WorktreeExitRequest
	exitRes WorktreeExitResult
	exitErr error

	listRes []WorktreeListEntry
	listErr error
}

func (h *stubWorktreeHost) WorktreeEnter(_ context.Context, req WorktreeEnterRequest) (WorktreeEnterResult, error) {
	h.enterReq = req
	return h.enterRes, h.enterErr
}

func (h *stubWorktreeHost) WorktreeExit(_ context.Context, req WorktreeExitRequest) (WorktreeExitResult, error) {
	h.exitReq = req
	return h.exitRes, h.exitErr
}

func (h *stubWorktreeHost) WorktreeList(context.Context) ([]WorktreeListEntry, error) {
	return h.listRes, h.listErr
}

func TestWorktreeToolNames(t *testing.T) {
	host := &stubWorktreeHost{}
	if got := NewWorktreeEnterTool(host).Name(); got != NameWorktreeEnter {
		t.Errorf("enter name = %q, want %q", got, NameWorktreeEnter)
	}
	if got := NewWorktreeExitTool(host).Name(); got != NameWorktreeExit {
		t.Errorf("exit name = %q, want %q", got, NameWorktreeExit)
	}
	if got := NewWorktreeListTool(host).Name(); got != NameWorktreeList {
		t.Errorf("list name = %q, want %q", got, NameWorktreeList)
	}
}

func TestWorktreeEnterToolParsesAndForwardsArguments(t *testing.T) {
	host := &stubWorktreeHost{enterRes: WorktreeEnterResult{Name: "feat-x", Branch: "chord/feat-x", Path: "/tmp/wt"}}
	tool := NewWorktreeEnterTool(host)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{
		"name": "  feat-x  ",
		"path": "  /tmp/wt  ",
		"base": "  abc1234  ",
		"branch": "  chord/feat-x  ",
		"reset_branch": true
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := WorktreeEnterRequest{Name: "feat-x", Path: "/tmp/wt", Base: "abc1234", Branch: "chord/feat-x", ResetBranch: true}
	if host.enterReq != want {
		t.Errorf("request = %#v, want %#v", host.enterReq, want)
	}
	if !strings.Contains(out, "Created and entered worktree feat-x") {
		t.Errorf("output = %q, want the create wording", out)
	}
}

func TestWorktreeEnterToolEmptyArgumentsCreateTypedRequest(t *testing.T) {
	host := &stubWorktreeHost{}
	tool := NewWorktreeEnterTool(host)
	if _, err := tool.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute(nil): %v", err)
	}
	if host.enterReq != (WorktreeEnterRequest{}) {
		t.Errorf("request = %#v, want the zero request", host.enterReq)
	}
}

func TestWorktreeEnterToolReportsResumedWorktree(t *testing.T) {
	host := &stubWorktreeHost{enterRes: WorktreeEnterResult{Name: "feat-x", Existed: true, Path: "/tmp/wt"}}
	out, err := NewWorktreeEnterTool(host).Execute(context.Background(), json.RawMessage(`{"name":"feat-x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Entered existing worktree feat-x") {
		t.Errorf("output = %q, want the resume wording", out)
	}
}

func TestWorktreeEnterToolRejectsInvalidArguments(t *testing.T) {
	host := &stubWorktreeHost{}
	if _, err := NewWorktreeEnterTool(host).Execute(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
	if host.enterReq != (WorktreeEnterRequest{}) {
		t.Errorf("host should not be called on a parse error, got %#v", host.enterReq)
	}
}

func TestWorktreeExitToolActionMapping(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantRemove bool
		wantErr    bool
	}{
		{name: "empty args default keep", raw: ``, wantRemove: false},
		{name: "explicit keep", raw: `{"action":"keep"}`, wantRemove: false},
		{name: "case-insensitive remove", raw: `{"action":" REMOVE "}`, wantRemove: true},
		{name: "unknown action", raw: `{"action":"drop"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := &stubWorktreeHost{}
			tool := NewWorktreeExitTool(host)
			out, err := tool.Execute(context.Background(), json.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "expected keep or remove") {
					t.Fatalf("err = %v, want an action validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if host.exitReq.Remove != tc.wantRemove {
				t.Errorf("Remove = %v, want %v", host.exitReq.Remove, tc.wantRemove)
			}
			if host.exitReq.DiscardChanges {
				t.Error("DiscardChanges should default to false")
			}
			if out == "" {
				t.Error("expected a non-empty result message")
			}
		})
	}
}

func TestWorktreeExitToolForwardsNameAndDiscard(t *testing.T) {
	host := &stubWorktreeHost{exitRes: WorktreeExitResult{Name: "feat-x", Branch: "chord/feat-x", WorkDir: "/repo", Removed: true}}
	out, err := NewWorktreeExitTool(host).Execute(context.Background(), json.RawMessage(`{
		"name": "  feat-x ",
		"action": "remove",
		"discard_changes": true
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := WorktreeExitRequest{Name: "feat-x", Remove: true, DiscardChanges: true}
	if host.exitReq != want {
		t.Errorf("request = %#v, want %#v", host.exitReq, want)
	}
	if !strings.Contains(out, "Removed worktree feat-x (branch chord/feat-x kept)") ||
		!strings.Contains(out, "now working in /repo") {
		t.Errorf("output = %q, want the removal and workdir wording", out)
	}
}

func TestWorktreeExitToolKeepsBranchWording(t *testing.T) {
	host := &stubWorktreeHost{exitRes: WorktreeExitResult{Name: "feat-x", Branch: "chord/feat-x", WorkDir: "/repo"}}
	out, err := NewWorktreeExitTool(host).Execute(context.Background(), json.RawMessage(`{"name":"feat-x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Left worktree feat-x (branch chord/feat-x kept)") {
		t.Errorf("output = %q, want the keep wording", out)
	}
}

func TestWorktreeListToolEmptyRepository(t *testing.T) {
	host := &stubWorktreeHost{}
	out, err := NewWorktreeListTool(host).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "No chord-managed worktrees in this repository." {
		t.Errorf("output = %q", out)
	}
}

func TestWorktreeListToolRendersOwnerAndStatus(t *testing.T) {
	host := &stubWorktreeHost{listRes: []WorktreeListEntry{
		{
			Name:           "feat-a",
			Branch:         "chord/feat-a",
			Path:           "/state/worktrees/repo/feat-a",
			OwnerKnown:     true,
			OwnerKind:      "main",
			OwnerSessionID: "0123456789abcdef",
			OwnerAgentID:   "agent-7",
			DirtyKnown:     true,
			Dirty:          true,
			Current:        true,
		},
		{Name: "feat-b", Branch: "chord/feat-b", Path: "/state/worktrees/repo/feat-b", DirtyKnown: true},
	}}
	out, err := NewWorktreeListTool(host).Execute(context.Background(), json.RawMessage(``))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"2 chord-managed worktree(s):",
		"* feat-a",
		"owner:  main:01234567/agent-7",
		"status: dirty",
		" feat-b",
		"owner:  unknown (created outside chord); remove it with `chord worktree remove`",
		"status: clean",
		"* = the agent's active working directory",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestWorktreeToolsUnavailableWithoutHost(t *testing.T) {
	if NewWorktreeEnterTool(nil).IsAvailable() {
		t.Error("enter tool should be unavailable without a host")
	}
	if NewWorktreeExitTool(nil).IsAvailable() {
		t.Error("exit tool should be unavailable without a host")
	}
	if NewWorktreeListTool(nil).IsAvailable() {
		t.Error("list tool should be unavailable without a host")
	}
	for _, tool := range []Tool{NewWorktreeEnterTool(nil), NewWorktreeExitTool(nil), NewWorktreeListTool(nil)} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Errorf("%s should fail without a host", tool.Name())
		}
	}
}

func TestWorktreeToolConcurrencyClasses(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewWorktreeEnterTool(&stubWorktreeHost{}))
	registry.Register(NewWorktreeExitTool(&stubWorktreeHost{}))
	registry.Register(NewWorktreeListTool(&stubWorktreeHost{}))

	if got := ConcurrencyClassForTool(registry, NameWorktreeList, json.RawMessage(`{}`)); got != ToolConcurrencyClassReadOnly {
		t.Errorf("list class = %v, want read-only", got)
	}
	for _, name := range []string{NameWorktreeEnter, NameWorktreeExit} {
		if got := ConcurrencyClassForTool(registry, name, json.RawMessage(`{}`)); got == ToolConcurrencyClassReadOnly {
			t.Errorf("%s class = %v, want a non-read-only class so the switch owns its own batch", name, got)
		}
	}
}
