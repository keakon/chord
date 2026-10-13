package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

func TestTakeoverCLIRejectsOversizedIdentityWithBoundedJSON(t *testing.T) {
	for _, flag := range []string{"--session", "--transfer-id", "--target-instance"} {
		t.Run(flag, func(t *testing.T) {
			args := []string{"start", "--session", "session-1", "--transfer-id", "transfer-1", "--target-kind", takeover.OwnerGateway, "--target-instance", "target-1", "--json"}
			for i := range args {
				if args[i] == flag {
					args[i+1] = strings.Repeat("x", takeover.MaxFrameBytes*2)
				}
			}
			cmd := newTakeoverCmd()
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(args)
			err := cmd.Execute()
			if takeoverCLIErrorCode(err) != takeover.CodeInvalidRequest || stdout.Len() > takeover.MaxFrameBytes || bytes.Count(stdout.Bytes(), []byte{'\n'}) != 1 {
				t.Fatalf("err=%v stdout length=%d", err, stdout.Len())
			}
			var status takeover.Status
			if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.ErrorCode != takeover.CodeInvalidRequest || len(status.TransferID) > 128 || len(status.SessionID) > 128 || len(status.TargetInstanceID) > 128 {
				t.Fatalf("unbounded error status=%+v", status)
			}
			if flag != "--transfer-id" && status.TransferID != "transfer-1" {
				t.Fatal("valid original transfer identity lost")
			}
		})
	}
}

func TestTakeoverCLIStatusEncodeDoesNotPartiallyWriteOversizedOutput(t *testing.T) {
	var out bytes.Buffer
	status := agent.SessionPauseStatus{Error: strings.Repeat("\x00", takeover.MaxFrameBytes), ErrorCode: takeover.CodeStatusUnknown}
	if err := writeTakeoverCLIStatus(&out, status); err != nil || out.Len() >= takeover.MaxFrameBytes {
		t.Fatalf("bounded message failed err=%v size=%d", err, out.Len())
	}
	out.Reset()
	status.Workspace = strings.Repeat("x", takeover.MaxFrameBytes)
	if err := writeTakeoverCLIStatus(&out, status); takeoverCLIErrorCode(err) != takeover.CodeInvalidRequest || out.Len() != 0 {
		t.Fatalf("oversized status was partially written err=%v bytes=%d", err, out.Len())
	}
}

func TestTakeoverCLIErrorCodesRemainBoundedAndTyped(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("context: %w", &takeover.Error{Code: takeover.CodeStaleSource, Message: "Source changed"}), takeover.CodeStaleSource},
		{&agent.SessionPauseError{Code: takeover.CodePersistenceFailed, Cause: errors.New("Save failed")}, takeover.CodePersistenceFailed},
		{errors.New(strings.Repeat("x", takeover.MaxFrameBytes) + ": detail"), takeover.CodeInvalidRequest},
		{&takeover.Error{Code: strings.Repeat("x", takeover.MaxFrameBytes), Message: "Invalid code"}, takeover.CodeInvalidRequest},
	} {
		if code := takeoverCLIErrorCode(tc.err); code != tc.code {
			t.Fatalf("code=%q expected=%q", code, tc.code)
		}
	}
}

func TestTakeoverStartRejectedBeforeDispatchPreservesIdentity(t *testing.T) {
	negative := -time.Second
	loc := takeoverLocation{workspace: "/workspace"}
	status, err := startTakeover(t.Context(), loc, "session-1", "transfer-1", takeover.OwnerTUI, "target-1", &negative)
	if err == nil || status.TransferID != "transfer-1" || status.SessionID != "session-1" || status.Workspace != loc.workspace || status.TargetInstanceID != "target-1" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestTakeoverCLIStatusSupportsBlockerPages(t *testing.T) {
	if !takeover.Supported() {
		t.Skip("takeover requires macOS or Linux")
	}
	dir, err := os.MkdirTemp("/tmp", "cp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace, err := takeover.CanonicalWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	record := takeover.Record{Protocol: takeover.ProtocolVersion, RuntimeID: "runtime-page", SessionID: "session-1", Workspace: workspace, OwnerKind: takeover.OwnerTUI}
	var blockers []agent.SessionPauseBlocker
	for i := range 130 {
		blockers = append(blockers, agent.SessionPauseBlocker{OwnerTaskID: fmt.Sprintf("task-%d", i), Kind: "tool", Description: "Active tool"})
	}
	server, err := takeover.StartServer(takeover.ServerOptions{StateDir: dir, Record: record,
		Start: func(context.Context, recovery.TransferIdentity) (agent.SessionPauseStatus, error) {
			return agent.SessionPauseStatus{}, nil
		},
		Status: func(transferID string) (agent.SessionPauseStatus, error) {
			return agent.SessionPauseStatus{TransferIdentity: recovery.TransferIdentity{TransferID: transferID, SessionID: record.SessionID, SourceRuntimeID: record.RuntimeID, Workspace: workspace, TargetKind: takeover.OwnerGateway, TargetInstanceID: "target-1"}, State: agent.SessionPauseDraining, Blockers: blockers, BlockerCount: len(blockers)}, nil
		},
		RetrySave: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	page, err := queryTakeoverPage(t.Context(), takeoverLocation{stateDir: dir, workspace: workspace}, "session-1", "transfer-1", 128)
	if err != nil || page.BlockerOffset != 128 || len(page.Blockers) != 2 || page.BlockerCount != 130 || page.NextOffset != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	var out bytes.Buffer
	if err := writeTakeoverCLIPublicStatus(&out, page); err != nil {
		t.Fatal(err)
	}
	var decoded takeover.Status
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.BlockerOffset != 128 || len(decoded.Blockers) != 2 {
		t.Fatalf("output=%s err=%v", out.Bytes(), err)
	}
	cmd := newTakeoverCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"status", "--session", "session-1", "--transfer-id", "transfer-1", "--blocker-offset", "-1", "--json"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("negative page accepted")
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.ErrorCode != takeover.CodeInvalidRequest {
		t.Fatalf("invalid page output=%s err=%v", out.Bytes(), err)
	}
}
