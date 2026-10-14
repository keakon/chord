// Run with GEMINI_API_KEY set. Artifacts contain no credentials or raw protocol bodies.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type observedTransport struct {
	base                http.RoundTripper
	requests            atomic.Int64
	checkpoints         atomic.Int64
	injected            atomic.Bool
	faultRequest        atomic.Int64
	recoveryRequest     atomic.Int64
	cancellationRequest atomic.Int64
	cut                 bool
	cancelAtCheckpoint  bool
	cancel              context.CancelFunc
}

func (t *observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	request := t.requests.Add(1)
	if t.injected.Load() {
		t.recoveryRequest.CompareAndSwap(0, request)
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		response.Body = &observedBody{source: response.Body, reader: bufio.NewReader(response.Body), owner: t, request: request}
	}
	return response, nil
}

type observedBody struct {
	source              io.ReadCloser
	reader              *bufio.Reader
	owner               *observedTransport
	pending             []byte
	sawCheckpoint, fail bool
	cancelPending       bool
	request             int64
}

func (b *observedBody) Close() error { return b.source.Close() }
func (b *observedBody) Read(p []byte) (int, error) {
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	if b.fail {
		b.owner.faultRequest.Store(b.request)
		b.owner.injected.Store(true)
		return 0, io.ErrUnexpectedEOF
	}
	// The previous Read returned a complete checkpoint line. Cancelling on
	// this next read lets the provider consume it before the parent stops.
	if b.cancelPending {
		b.owner.cancellationRequest.Store(b.request)
		b.owner.cancel()
		return 0, context.Canceled
	}
	line, err := b.reader.ReadBytes('\n')
	if len(line) > 16<<20 {
		return 0, errors.New("SSE line exceeds observation limit")
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	var chunk struct {
		Candidates []struct {
			Token        string `json:"continuationToken"`
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(data, &chunk) == nil {
		for _, candidate := range chunk.Candidates {
			if candidate.Token != "" && (candidate.FinishReason == "" || candidate.FinishReason == "CONTINUATION") {
				b.sawCheckpoint = true
				b.owner.checkpoints.Add(1)
				if b.owner.cancelAtCheckpoint {
					b.cancelPending = true
				}
			}
			if b.sawCheckpoint && candidate.Token == "" && candidate.FinishReason == "" && b.owner.cut {
				for _, part := range candidate.Content.Parts {
					if part.Text != "" && !b.owner.injected.Load() {
						b.fail = true
					}
				}
			}
		}
	}
	n := copy(p, line)
	b.pending = line[n:]
	if n > 0 {
		return n, nil
	}
	return 0, err
}
func main() {
	maxOutput := flag.Int("max-output-tokens", 131072, "total generation budget")
	thinking := flag.String("thinking-level", "high", "thinking level")
	promptFile := flag.String("prompt-file", "", "neutral proof task file")
	timeout := flag.Duration("timeout", 45*time.Minute, "parent timeout")
	cut := flag.Bool("cut-after-checkpoint", false, "inject one read failure after checkpoint and pending output")
	cancelAtCheckpoint := flag.Bool("cancel-after-checkpoint", false, "cancel the parent after a checkpoint")
	artifacts := flag.String("artifacts", "/tmp/chord-gemini-continuation-live", "sanitized artifacts directory")
	flag.Parse()
	if err := run(*maxOutput, *thinking, *promptFile, *timeout, *cut, *cancelAtCheckpoint, *artifacts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(maxOutput int, thinking, promptFile string, timeout time.Duration, cut, cancelAtCheckpoint bool, artifacts string) error {
	if cut && cancelAtCheckpoint {
		return errors.New("choose either fault injection or checkpoint cancellation")
	}
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return errors.New("GEMINI_API_KEY is required")
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		return fmt.Errorf("read prompt: %w", err)
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	transport := &observedTransport{base: http.DefaultTransport, cut: cut, cancelAtCheckpoint: cancelAtCheckpoint, cancel: cancel}
	cfg := llm.NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: "https://generativelanguage.googleapis.com/v1beta", ResponseHeaderTimeout: 1200, StreamIdleTimeout: 1200, Models: map[string]config.ModelConfig{"gemini-4-argon": {Limit: config.ModelLimit{Context: 1048576, Output: 1048576}, Thinking: &config.ThinkingConfig{Level: thinking}, Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true), SingleRequestOutputLimit: new(65536)}}}}}, []string{key})
	provider, err := llm.NewGeminiProviderWithClient(cfg, &http.Client{Transport: transport}, "")
	if err != nil {
		return err
	}
	client := llm.NewClient(cfg, provider, "gemini-4-argon", maxOutput, "")
	defer client.Close()
	client.SetOutputTokenMax(maxOutput)
	client.SetStreamRetryRounds(1)
	var preview strings.Builder
	type timedReceipt struct {
		llm.ContinuationUsage
		SettledAt time.Time `json:"settled_at"`
	}
	var receipts []timedReceipt
	var billing message.TokenUsage
	billingComplete := true
	started := time.Now()
	response, callErr := client.CompleteStreamWithOptions(ctx, []message.Message{{Role: message.RoleUser, Content: string(prompt)}}, nil, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaRollback {
			preview.Reset()
		}
		if delta.Type == message.StreamDeltaText {
			preview.WriteString(delta.Text)
		}
	}, llm.CompleteStreamOptions{OnContinuationUsage: func(receipt llm.ContinuationUsage) {
		receipts = append(receipts, timedReceipt{ContinuationUsage: receipt, SettledAt: time.Now()})
		if u := receipt.Usage; u != nil {
			billing.InputTokens += u.InputTokens
			billing.OutputTokens += u.OutputTokens
			billing.CacheReadTokens += u.CacheReadTokens
			billing.ReasoningTokens += u.ReasoningTokens
		}
		if receipt.UsageState != contextdiag.Reported {
			billingComplete = false
		}
	}})
	manifest := map[string]any{"started_at": started, "duration": time.Since(started).String(), "requests": transport.requests.Load(), "checkpoints": transport.checkpoints.Load(), "fault_injected": transport.injected.Load(), "receipts": receipts, "billing_usage": billing, "billing_complete": billingComplete, "max_output_tokens": maxOutput, "thinking_level": thinking, "fault_request": transport.faultRequest.Load(), "recovery_request": transport.recoveryRequest.Load(), "cancellation_request": transport.cancellationRequest.Load()}
	verdict := evaluateAcceptance(transport, response, callErr, preview.String())
	manifest["acceptance"] = verdict
	if response != nil {
		manifest["usage"] = response.Usage
		manifest["preview_matches"] = verdict.PreviewMatches
		manifest["stop_reason"] = response.StopReason
		manifest["long_decode_verified"] = verdict.LongDecodeVerified
		if err := os.WriteFile(filepath.Join(artifacts, "final.txt"), []byte(response.Content), 0600); err != nil {
			return err
		}
	}
	if callErr != nil {
		manifest["error"] = strings.ReplaceAll(callErr.Error(), key, "[redacted]")
	}
	if err := os.WriteFile(filepath.Join(artifacts, "preview.txt"), []byte(preview.String()), 0600); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(artifacts, "manifest.json"), data, 0600); err != nil {
		return err
	}
	fmt.Printf("requests=%d checkpoints=%d fault_injected=%v artifacts=%s\n", transport.requests.Load(), transport.checkpoints.Load(), transport.injected.Load(), artifacts)
	if cut && !verdict.RecoveryVerified {
		return errors.New("recovery sample not verified; see sanitized manifest")
	}
	if cancelAtCheckpoint {
		if !verdict.CancellationVerified {
			return errors.New("cancellation sample not verified; see sanitized manifest")
		}
		return nil
	}
	if callErr != nil || !verdict.PreviewMatches {
		return errors.New("live acceptance failed; see sanitized manifest")
	}
	return nil
}

type acceptanceResult struct {
	PreviewMatches       bool `json:"preview_matches"`
	LongDecodeVerified   bool `json:"long_decode_verified"`
	RecoveryVerified     bool `json:"recovery_verified"`
	CancellationVerified bool `json:"cancellation_verified"`
}

func evaluateAcceptance(transport *observedTransport, response *message.Response, err error, preview string) acceptanceResult {
	result := acceptanceResult{PreviewMatches: response != nil && response.Content == preview}
	success := err == nil && result.PreviewMatches
	result.LongDecodeVerified = success && transport.requests.Load() >= 2 && response.Usage != nil && response.Usage.Context != nil && response.Usage.Context.Known && response.Usage.Context.OutputTokens > 65536
	result.RecoveryVerified = success && transport.injected.Load() && transport.recoveryRequest.Load() > transport.faultRequest.Load()
	result.CancellationVerified = errors.Is(err, context.Canceled) && transport.cancellationRequest.Load() > 0 && transport.requests.Load() == transport.cancellationRequest.Load()
	return result
}
