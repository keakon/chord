package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

const headlessReceiptHelperEnv = "CHORD_HEADLESS_RECEIPT_HELPER"

type cancellingInputBackend struct {
	mockBackend
	cancel context.CancelFunc
}

func (b *cancellingInputBackend) SendUserMessageWithReceipt(string, string) bool {
	b.cancel()
	return false
}

func TestHeadlessInputRejectionProcess(t *testing.T) {
	if os.Getenv(headlessReceiptHelperEnv) == "1" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: "session-sample"}
		events := make(chan agent.AgentEvent)
		close(events)
		rt := &fakeHeadlessRuntime{events: events, backend: &cancellingInputBackend{cancel: cancel}}
		err := runHeadlessWithDeps(headlessRunDeps{
			initApp:       func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
			createRuntime: func(*AppContext) (headlessRuntime, error) { return rt, nil },
			stdin:         os.Stdin,
			stdout:        os.Stdout,
		})
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHeadlessInputRejectionProcess$")
	cmd.Env = append(os.Environ(), headlessReceiptHelperEnv+"=1")
	cmd.Stdin = strings.NewReader("{\"type\":\"send\",\"content\":\"sample\",\"request_id\":\"input-1\"}\n")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("headless subprocess failed: %v", err)
	}
	var replies int
	for _, env := range decodeHeadlessJSONLines(t, output) {
		if env.Type != "input_result" {
			continue
		}
		replies++
		payload := env.Payload.(map[string]any)
		if payload["request_id"] != "input-1" || payload["status"] != agent.InputRejected {
			t.Fatalf("unexpected receipt: %#v", env)
		}
	}
	if replies != 1 {
		t.Fatalf("rejection receipts = %d, want 1", replies)
	}
}
