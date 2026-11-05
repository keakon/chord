package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestPatchCleanedPathsMatchResourcesAndExecution(t *testing.T) {
	for _, suffix := range []string{"\ufe0f", "\u200b"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "sample.txt")
			if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			registry.Register(tools.ApplyPatchTool{BaseDir: dir})
			makeCall := func(id, path, before, after string) message.ToolCall {
				args, err := json.Marshal(tools.ApplyPatchArgs{
					Patch: "*** Begin Patch\n*** Update File: " + path + "\n@@\n-" + before + "\n+" + after + "\n*** End Patch\n",
				})
				if err != nil {
					t.Fatal(err)
				}
				// JSON escapes survive response-level text cleaning.
				args = []byte(strings.ReplaceAll(string(args), "\u200b", `\u200b`))
				return message.ToolCall{ID: id, Name: tools.NameApplyPatch, Args: args}
			}
			response := &message.Response{ToolCalls: []message.ToolCall{
				makeCall("first", "sample"+suffix+".txt", "before", "middle"),
				makeCall("second", "sample.txt", "middle", "after"),
			}}
			sanitizeResponseZeroWidth(response)
			pipeline := toolExecutionPipeline{
				registry: registry, toolBaseDir: dir, agentID: "writer",
				fileTrack: filelock.NewFileTracker(), governor: newResourceGovernor(config.OrchestrationConfig{}),
			}
			first := pipeline.concurrencyPolicyForCall(response.ToolCalls[0])
			second := pipeline.concurrencyPolicyForCall(response.ToolCalls[1])
			if !first.LeaseConflicts(second) {
				t.Fatalf("same execution target has disjoint leases: %v / %v", first.Resources, second.Resources)
			}
			batches := pipeline.buildToolExecutionBatches(response.ToolCalls)
			if len(batches) != 2 || batches[0].Calls[0].ID != "first" || batches[1].Calls[0].ID != "second" {
				t.Fatalf("conflicting patches must preserve response order: %+v", batches)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			for _, batch := range batches {
				result, err := pipeline.execute(ctx, batch.Calls[0], false)
				if err != nil {
					t.Fatal(err)
				}
				if result.FileState == nil || len(result.FileState.Writes) != 1 || result.FileState.Writes[0].Path != target {
					t.Fatalf("file state did not record the execution target: %+v", result.FileState)
				}
				if batch.Calls[0].ID == "first" && !strings.Contains(result.Result, "cleaned 1 invisible character") {
					t.Fatalf("cleanup note missing: %s", result.Result)
				}
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "after\n" {
				t.Fatalf("target = %q, err = %v", content, err)
			}
		})
	}
}

func TestPatchPermissionUsesCleanedSourceAndDestination(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameApplyPatch, Pattern: "secret/*", Action: permission.ActionDeny},
		{Permission: tools.NameDelete, Pattern: "remove/*", Action: permission.ActionDeny},
		{Permission: tools.NameWrite, Pattern: "write/*", Action: permission.ActionDeny},
	}
	for _, patch := range []string{
		"*** Add File: secret\ufe0f/sample.txt\n+sample\n",
		"*** Delete File: remove\ufe0f/sample.txt\n",
		"*** Update File: sample.txt\n*** Move to: write\u200b/sample.txt\n@@\n-before\n+after\n",
	} {
		args, err := json.Marshal(tools.ApplyPatchArgs{Patch: "*** Begin Patch\n" + patch + "*** End Patch\n"})
		if err != nil {
			t.Fatal(err)
		}
		decision := evaluateToolPermissionInDir(ruleset, tools.NameApplyPatch, args, permission.PathScope{})
		if decision.Action != permission.ActionDeny {
			t.Fatalf("cleaned patch target bypassed deny rule: %+v", decision)
		}
	}
}

func TestDeleteSymlinkAncestorPreservesResponseOrder(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "sample.txt")
	if err := os.WriteFile(target, []byte("sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.DeleteTool{BaseDir: dir})
	var calls []message.ToolCall
	for _, path := range []string{"link/sample.txt", "link"} {
		args, err := json.Marshal(tools.DeleteRequest{Paths: []string{path}, Reason: "cleanup"})
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, message.ToolCall{ID: path, Name: tools.NameDelete, Args: args})
	}
	pipeline := toolExecutionPipeline{
		registry: registry, toolBaseDir: dir, agentID: "writer",
		fileTrack: filelock.NewFileTracker(), governor: newResourceGovernor(config.OrchestrationConfig{}),
	}
	if !pipeline.concurrencyPolicyForCall(calls[0]).LeaseConflicts(pipeline.concurrencyPolicyForCall(calls[1])) {
		t.Fatal("deleting an ancestor symlink must conflict with its child")
	}
	batches := pipeline.buildToolExecutionBatches(calls)
	if len(batches) != 2 || batches[0].Calls[0].ID != calls[0].ID {
		t.Fatalf("ancestor deletion reordered: %+v", batches)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for _, batch := range batches {
		result, err := pipeline.execute(ctx, batch.Calls[0], false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Result, "Deleted (1)") {
			t.Fatalf("target was not deleted: %s", result.Result)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("child remains after both deletions: %v", err)
	}
}
