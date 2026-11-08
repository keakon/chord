package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// TestHeadlessStrictInputContract drives the command loop in-process: strict
// v2 decoding rejects duplicate object members and invalid UTF-8 with the
// productized error envelope instead of silently resolving them.
func TestHeadlessStrictInputContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: "session-sample"}
	events := make(chan agent.AgentEvent)
	close(events)
	rt := &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}

	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	var mu sync.Mutex
	var envelopes []headlessEnvelope
	collectorDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var env headlessEnvelope
			if json.Unmarshal(line, &env) != nil {
				continue
			}
			mu.Lock()
			envelopes = append(envelopes, env)
			mu.Unlock()
		}
		collectorDone <- scanner.Err()
	}()

	input := "{\"type\":\"status\",\"type\":\"subscribe\"}\n" + "{\"type\":\"stat\xffus\"}\n"
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessWithDeps(headlessRunDeps{
			initApp:       func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
			createRuntime: func(*AppContext) (headlessRuntime, error) { return rt, nil },
			stdin:         strings.NewReader(input),
			stdout:        pw,
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("headless loop failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("headless loop did not drain stdin within budget")
	}
	pw.Close()
	select {
	case err := <-collectorDone:
		if err != nil {
			t.Fatalf("output collector failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("output collector did not stop")
	}

	invalid := 0
	mu.Lock()
	defer mu.Unlock()
	for _, env := range envelopes {
		if env.Type != "error" {
			continue
		}
		if payload, ok := env.Payload.(map[string]any); ok && payload["message"] == "invalid JSON command" {
			invalid++
		}
	}
	if invalid != 2 {
		t.Fatalf("invalid JSON command envelopes = %d, want 2 (got %#v)", invalid, envelopes)
	}
}
