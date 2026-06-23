package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

func testDoctorModelsConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {
				Type: config.ProviderTypeMessages,
				Models: map[string]config.ModelConfig{
					"claude": {Limit: config.ModelLimit{Context: 1000, Output: 128}},
				},
			},
			"local": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"a-model": {Limit: config.ModelLimit{Context: 1000, Output: 128}},
					"z-model": {Limit: config.ModelLimit{Context: 1000, Output: 128}},
				},
			},
			"openai": {
				Type: config.ProviderTypeResponses,
				Models: map[string]config.ModelConfig{
					"gpt": {
						Limit: config.ModelLimit{Context: 1000, Output: 128},
						Variants: map[string]config.ModelVariant{
							"high": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
						},
					},
				},
			},
		},
		ModelPools: map[string][]string{
			"thinking": {"openai/gpt@high", "anthropic/claude"},
		},
	}
}

func setupDoctorModelsConfigHome(t *testing.T, configYAML, authYAML string) string {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if authYAML != "" {
		if err := os.WriteFile(filepath.Join(configHome, "auth.yaml"), []byte(authYAML), 0o600); err != nil {
			t.Fatalf("write auth: %v", err)
		}
	}
	return configHome
}

func writeDoctorChatCompletionSSE(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func writeDoctorResponsesSSE(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("event: response.output_text.delta\n"))
	_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"content_index\":0,\"delta\":\"hello\",\"item_id\":\"msg_1\",\"logprobs\":[],\"output_index\":0}\n\n"))
	_, _ = w.Write([]byte("event: response.completed\n"))
	_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func TestLoadDoctorModelsRuntimeConfigMissingConfigReturnsInitialSetupError(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	cfg, err := loadDoctorModelsRuntimeConfig()
	if err == nil {
		t.Fatal("expected missing-config error")
	}
	if cfg != nil {
		t.Fatalf("expected nil config on error, got %#v", cfg)
	}
	if err.Error() != initialSetupRequiredMessage {
		t.Fatalf("error = %q, want %q", err, initialSetupRequiredMessage)
	}
}

func TestDoctorModelsPlanRepresentativeSelection(t *testing.T) {
	cfg := testDoctorModelsConfig()
	plan, err := buildDoctorModelsPlan(cfg, []string{"thinking"}, doctorModelsOptions{})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if plan.Mode != doctorModelsModeProviderRepresentatives {
		t.Fatalf("mode = %q", plan.Mode)
	}
	var got []string
	for _, entry := range plan.Entries {
		if entry.Target == nil {
			t.Fatalf("unexpected config result: %+v", entry.Result)
		}
		got = append(got, entry.Target.CanonicalRef)
	}
	want := []string{"anthropic/claude", "local/a-model", "openai/gpt@high"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
}

func TestDoctorModelsPlanRepresentativeSkipsUnknownPoolModels(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"sinohealth": {
				Type: config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{
					"glm-5": {Limit: config.ModelLimit{Context: 1000, Output: 128}},
				},
			},
		},
		ModelPools: map[string][]string{
			"default": {"sinohealth/mimo-v2.5-pro"},
		},
	}

	plan, err := buildDoctorModelsPlan(cfg, []string{"default"}, doctorModelsOptions{Provider: "sinohealth"})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Target == nil {
		t.Fatalf("entries = %+v, want 1 target", plan.Entries)
	}
	if got := plan.Entries[0].Target.CanonicalRef; got != "sinohealth/glm-5" {
		t.Fatalf("canonical ref = %q, want sinohealth/glm-5", got)
	}
}

func TestDoctorModelsPlanNoProvidersIsConfigError(t *testing.T) {
	plan, err := buildDoctorModelsPlan(&config.Config{}, nil, doctorModelsOptions{})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Result == nil {
		t.Fatalf("entries = %+v, want config error result", plan.Entries)
	}
	result := plan.Entries[0].Result
	if result.Status != doctorModelResultConfigError || !strings.Contains(result.Error, "no providers configured") {
		t.Fatalf("result = %+v, want no-providers config error", result)
	}
}

func TestDoctorModelsPlanExplicitModelRules(t *testing.T) {
	cfg := testDoctorModelsConfig()

	if _, err := buildDoctorModelsPlan(cfg, nil, doctorModelsOptions{ModelRef: "gpt@high"}); err == nil || !strings.Contains(err.Error(), "does not include a provider") {
		t.Fatalf("bare model error = %v, want missing provider", err)
	}

	if _, err := buildDoctorModelsPlan(cfg, nil, doctorModelsOptions{Provider: "openai", ModelRef: "anthropic/claude"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("provider mismatch error = %v, want mismatch", err)
	}

	plan, err := buildDoctorModelsPlan(cfg, nil, doctorModelsOptions{ModelRef: "openai/gpt@missing"})
	if err != nil {
		t.Fatalf("variant config error should be represented in plan, got err %v", err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Result == nil || plan.Entries[0].Result.Status != doctorModelResultConfigError {
		t.Fatalf("variant entry = %+v, want config error result", plan.Entries)
	}
}

func TestDoctorModelsPlanProviderAllowsSlashModelIDs(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"openrouter": {
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"anthropic/claude": {
					Limit: config.ModelLimit{Context: 1000, Output: 128},
					Variants: map[string]config.ModelVariant{
						"fast": {},
					},
				},
			},
		},
	}}
	plan, err := buildDoctorModelsPlan(cfg, nil, doctorModelsOptions{Provider: "openrouter", ModelRef: "anthropic/claude@fast"})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Target == nil {
		t.Fatalf("entries = %+v, want target", plan.Entries)
	}
	if got := plan.Entries[0].Target.CanonicalRef; got != "openrouter/anthropic/claude@fast" {
		t.Fatalf("canonical ref = %q", got)
	}
}

func TestDoctorModelsPlanPoolRejectsBareRefs(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {Type: config.ProviderTypeResponses, Models: map[string]config.ModelConfig{"gpt": {Limit: config.ModelLimit{Context: 1000, Output: 128}}}},
		},
		ModelPools: map[string][]string{"thinking": {"gpt", "openai/gpt"}},
	}
	plan, err := buildDoctorModelsPlan(cfg, []string{"thinking"}, doctorModelsOptions{Pool: "thinking"})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("entries len = %d", len(plan.Entries))
	}
	if plan.Entries[0].Result == nil || plan.Entries[0].Result.Status != doctorModelResultConfigError || !strings.Contains(plan.Entries[0].Result.Error, "does not include a provider") {
		t.Fatalf("first entry = %+v, want bare-ref config error", plan.Entries[0])
	}
	if plan.Entries[1].Target == nil || plan.Entries[1].Target.CanonicalRef != "openai/gpt" {
		t.Fatalf("second entry = %+v, want openai/gpt target", plan.Entries[1])
	}
}

func TestDoctorModelsValidateAllModelsRequiresProvider(t *testing.T) {
	err := validateDoctorModelsOptions(doctorModelsOptions{AllModels: true, Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "requires --provider") {
		t.Fatalf("error = %v, want requires --provider", err)
	}
}

func TestDoctorModelsValidateMutuallyExclusiveOptions(t *testing.T) {
	tests := []doctorModelsOptions{
		{ModelRef: "openai/gpt", Pool: "thinking", Timeout: time.Second},
		{ModelRef: "openai/gpt", AllPools: true, Timeout: time.Second},
		{Provider: "openai", Pool: "thinking", Timeout: time.Second},
		{Provider: "openai", AllPools: true, Timeout: time.Second},
		{Provider: "openai", AllModels: true, Pool: "thinking", Timeout: time.Second},
	}
	for _, opts := range tests {
		if err := validateDoctorModelsOptions(opts); err == nil {
			t.Fatalf("validateDoctorModelsOptions(%+v) error = nil, want error", opts)
		}
	}
}

func TestDoctorModelsValidateRetryMustBePositive(t *testing.T) {
	err := validateDoctorModelsOptions(doctorModelsOptions{Timeout: time.Second, Retry: -1})
	if err == nil || !strings.Contains(err.Error(), "--retry must be greater than zero") {
		t.Fatalf("error = %v, want positive retry validation", err)
	}
}

func TestDoctorModelsCommandRejectsExplicitZeroRetry(t *testing.T) {
	cmd := newDoctorCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"models", "--retry", "0"})
	err := cmd.Execute()
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 || !strings.Contains(err.Error(), "--retry must be greater than zero") {
		t.Fatalf("doctor models --retry 0 err = %v, want exit 2 retry validation", err)
	}
}

func TestShouldPrintCLIErrorSuppressesCanceledExit(t *testing.T) {
	if shouldPrintCLIError(cliExitError{code: 130, err: context.Canceled}) {
		t.Fatal("should not print CLI error for canceled exit")
	}
	if !shouldPrintCLIError(errors.New("boom")) {
		t.Fatal("should print CLI error for ordinary errors")
	}
}

func TestDoctorModelsCommandHelp(t *testing.T) {
	cmd := newDoctorCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"models", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor models --help: %v", err)
	}
	text := out.String()
	for _, want := range []string{"provider/model[@variant]", "--all-pools", "--fail-fast", "--json", "--retry"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help output missing %q:\n%s", want, text)
		}
	}
}

func TestRunDoctorModelsNoProvidersReturnsConfigError(t *testing.T) {
	setupDoctorModelsConfigHome(t, "{}\n", "")

	var out bytes.Buffer
	err := runDoctorModels(t.Context(), doctorModelsOptions{Timeout: time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("runDoctorModels err = %v, want exit 2\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Summary.ConfigErrors != 1 || len(report.Results) != 1 || !strings.Contains(report.Results[0].Error, "no providers configured") {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunDoctorModelsJSONAppliesVariantTuning(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configYAML := "providers:\n" +
		"  local:\n" +
		"    type: chat-completions\n" +
		"    api_url: " + server.URL + "/v1/chat/completions\n" +
		"    models:\n" +
		"      gpt:\n" +
		"        limit:\n" +
		"          context: 1000\n" +
		"          output: 128\n" +
		"        reasoning:\n" +
		"          effort: low\n" +
		"        text:\n" +
		"          verbosity: low\n" +
		"        variants:\n" +
		"          high:\n" +
		"            reasoning:\n" +
		"              effort: high\n" +
		"            text:\n" +
		"              verbosity: high\n"
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "auth.yaml"), []byte("local:\n  - test-key\n"), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	var out bytes.Buffer
	err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "local/gpt@high", Timeout: 5 * time.Second, JSON: true, Out: &out})
	if err != nil {
		if exitErr, ok := errors.AsType[cliExitError](err); ok {
			t.Fatalf("runDoctorModels exit %d: %v\noutput: %s", exitErr.code, exitErr.err, out.String())
		}
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high; body=%#v", got["reasoning_effort"], got)
	}
	if got["verbosity"] != "high" {
		t.Fatalf("verbosity = %#v, want high; body=%#v", got["verbosity"], got)
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Summary.Passed != 1 || len(report.Results) != 1 || report.Results[0].TextChunks != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Results[0].Usage == nil || report.Results[0].Usage.Input != 12 || report.Results[0].Usage.Output != 3 {
		t.Fatalf("usage = %+v", report.Results[0].Usage)
	}
}

func TestDoctorModelsPlanAllPoolsPreservesOrderAndPoolIndex(t *testing.T) {
	cfg := testDoctorModelsConfig()
	cfg.ModelPools = map[string][]string{
		"fallback": {"local/z-model", "openai/gpt@high"},
		"thinking": {"anthropic/claude"},
	}
	plan, err := buildDoctorModelsPlan(cfg, []string{"thinking", "fallback"}, doctorModelsOptions{AllPools: true})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if plan.Mode != doctorModelsModeAllPools || plan.PoolCount != 2 {
		t.Fatalf("plan mode/count = %q/%d", plan.Mode, plan.PoolCount)
	}
	wantRefs := []string{"anthropic/claude", "local/z-model", "openai/gpt@high"}
	wantPools := []string{"thinking", "fallback", "fallback"}
	wantIndexes := []int{0, 0, 1}
	for i, entry := range plan.Entries {
		if entry.Target == nil {
			t.Fatalf("entry %d = %+v, want target", i, entry)
		}
		if entry.Target.CanonicalRef != wantRefs[i] || entry.Target.PoolName != wantPools[i] || entry.Target.PoolIndex != wantIndexes[i] {
			t.Fatalf("entry %d target = %+v", i, entry.Target)
		}
	}
}

func TestDoctorModelsPlanAllModelsSortsTargets(t *testing.T) {
	cfg := testDoctorModelsConfig()
	plan, err := buildDoctorModelsPlan(cfg, nil, doctorModelsOptions{Provider: "local", AllModels: true})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if plan.Mode != doctorModelsModeProviderAllModels || plan.Provider != "local" {
		t.Fatalf("plan mode/provider = %q/%q", plan.Mode, plan.Provider)
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(plan.Entries))
	}
	got := make([]string, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		if entry.Target == nil {
			t.Fatalf("unexpected config result: %+v", entry.Result)
		}
		if entry.Target.Source != doctorModelSourceProviderAllModels {
			t.Fatalf("target source = %q, want %q", entry.Target.Source, doctorModelSourceProviderAllModels)
		}
		got = append(got, entry.Target.CanonicalRef)
	}
	want := []string{"local/a-model", "local/z-model"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
}

func TestDoctorModelsFailFastMarksRemainingPlanEntriesSkipped(t *testing.T) {
	cfg := testDoctorModelsConfig()
	cfg.ModelPools = map[string][]string{"thinking": {"gpt", "openai/gpt@high", "anthropic/claude"}}
	plan, err := buildDoctorModelsPlan(cfg, []string{"thinking"}, doctorModelsOptions{Pool: "thinking", FailFast: true})
	if err != nil {
		t.Fatalf("buildDoctorModelsPlan: %v", err)
	}
	if len(plan.Entries) != 3 {
		t.Fatalf("entries len = %d, want full plan length 3", len(plan.Entries))
	}
	report := executeDoctorModelsPlan(t.Context(), nil, plan, doctorModelsOptions{FailFast: true, Timeout: time.Second}, nil)
	if report.Summary.ConfigErrors != 1 || report.Summary.Skipped != 2 || len(report.Results) != 3 {
		t.Fatalf("report = %+v", report)
	}
	if report.Results[0].Status != doctorModelResultConfigError || report.Results[1].Status != doctorModelResultSkipped || report.Results[2].Status != doctorModelResultSkipped {
		t.Fatalf("results = %+v", report.Results)
	}
	if !strings.Contains(report.Results[1].Error, "--fail-fast") || !strings.Contains(report.Results[2].Error, "--fail-fast") {
		t.Fatalf("skipped errors = %q, %q; want fail-fast reason", report.Results[1].Error, report.Results[2].Error)
	}
}

func TestDoctorModelsCommandRejectsPositionalArgs(t *testing.T) {
	cmd := newDoctorCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"models", "unexpected"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("doctor models unexpected error = nil")
	}
	if !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error = %v, want mention unexpected arg", err)
	}
}

func TestDoctorModelsCommandAppliesAPIBaseOverride(t *testing.T) {
	var configHits atomic.Int32
	configServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		configHits.Add(1)
		http.Error(w, "configured endpoint should not be used", http.StatusInternalServerError)
	}))
	defer configServer.Close()

	var overrideHits atomic.Int32
	overrideServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overrideHits.Add(1)
		defer r.Body.Close()
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer overrideServer.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+configServer.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	oldAPIBase := flagAPIBase
	flagAPIBase = overrideServer.URL + "/v1/chat/completions"
	t.Cleanup(func() { flagAPIBase = oldAPIBase })

	cmd := newDoctorCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"models", "--model", "local/gpt", "--json", "--timeout", "5s"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("doctor models: %v\noutput: %s", err, out.String())
	}
	if overrideHits.Load() != 1 || configHits.Load() != 0 {
		t.Fatalf("override hits = %d, config hits = %d", overrideHits.Load(), configHits.Load())
	}
}

func TestRunDoctorModelsAllModelsTestsEveryConfiguredModel(t *testing.T) {
	var (
		mu        sync.Mutex
		gotModels []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		mu.Lock()
		gotModels = append(gotModels, body.Model)
		mu.Unlock()
		if body.Model == "bad" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"permission denied"}}`))
			return
		}
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      good:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n"+
		"      bad:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	var out bytes.Buffer
	err := runDoctorModels(t.Context(), doctorModelsOptions{Provider: "local", AllModels: true, Timeout: 5 * time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 1 {
		t.Fatalf("runDoctorModels err = %v, want exit 1\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Mode != doctorModelsModeProviderAllModels || report.Provider != "local" {
		t.Fatalf("report mode/provider = %q/%q", report.Mode, report.Provider)
	}
	if report.Summary.Failed != 1 || report.Summary.Passed != 1 || len(report.Results) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Results[0].CanonicalRef != "local/bad" || report.Results[0].Status != doctorModelResultFailed || report.Results[1].CanonicalRef != "local/good" || report.Results[1].Status != doctorModelResultSuccess {
		t.Fatalf("results = %+v", report.Results)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(gotModels, ",") != "bad,good" {
		t.Fatalf("models = %#v, want [bad good]", gotModels)
	}
}

func TestRunDoctorModelsPoolTestsEachRefIndependently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model == "bad" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"permission denied"}}`))
			return
		}
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      bad:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n"+
		"      good:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n"+
		"model_pools:\n"+
		"  audit:\n"+
		"    - local/bad\n"+
		"    - local/good\n", "local:\n  - test-key\n")

	var out bytes.Buffer
	err := runDoctorModels(t.Context(), doctorModelsOptions{Pool: "audit", Timeout: 5 * time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 1 {
		t.Fatalf("runDoctorModels err = %v, want exit 1\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Summary.Failed != 1 || report.Summary.Passed != 1 || len(report.Results) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Results[0].CanonicalRef != "local/bad" || report.Results[0].Status != doctorModelResultFailed || report.Results[1].CanonicalRef != "local/good" || report.Results[1].Status != doctorModelResultSuccess {
		t.Fatalf("results = %+v", report.Results)
	}
}

func TestRunDoctorModelsCanceledReturnsExit130(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out bytes.Buffer
	err := runDoctorModels(ctx, doctorModelsOptions{ModelRef: "local/gpt", Timeout: 5 * time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 130 {
		t.Fatalf("runDoctorModels err = %v, want exit 130\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if !report.Canceled {
		t.Fatalf("report.Canceled = false, want true; report = %+v", report)
	}
	if len(report.Results) != 1 || report.Results[0].Status != doctorModelResultFailed || !strings.Contains(strings.ToLower(report.Results[0].Error), "context canceled") {
		t.Fatalf("results = %+v", report.Results)
	}
}

func TestRunDoctorModelsDoesNotRetryAuthOrClientErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				defer r.Body.Close()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"diagnostic client/auth failure","type":"invalid_request_error"}}`))
			}))
			defer server.Close()

			setupDoctorModelsConfigHome(t, "providers:\n"+
				"  local:\n"+
				"    type: chat-completions\n"+
				"    api_url: "+server.URL+"/v1/chat/completions\n"+
				"    models:\n"+
				"      gpt:\n"+
				"        limit:\n"+
				"          context: 1000\n"+
				"          output: 128\n", "local:\n  - bad-key\n  - second-key\n")

			var out bytes.Buffer
			err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "local/gpt", Timeout: 5 * time.Second, Retry: 3, JSON: true, Out: &out})
			if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 1 {
				t.Fatalf("runDoctorModels err = %v, want exit 1\noutput: %s", err, out.String())
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1 for HTTP %d", requests.Load(), status)
			}
		})
	}
}

func TestRunDoctorModelsRetryAllowsTransientRecovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary upstream failure"}}`))
			return
		}
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "local/gpt", Timeout: 5 * time.Second, Retry: 2, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Summary.Passed != 1 || len(report.Results) != 1 || report.Results[0].Status != doctorModelResultSuccess {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunDoctorModelsCanceledSkipsRemainingTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      a:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n"+
		"      b:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out bytes.Buffer
	err := runDoctorModels(ctx, doctorModelsOptions{Provider: "local", AllModels: true, Timeout: 5 * time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 130 {
		t.Fatalf("runDoctorModels err = %v, want exit 130\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if !report.Canceled {
		t.Fatalf("report.Canceled = false, want true; report = %+v", report)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(report.Results))
	}
	if report.Results[0].Status != doctorModelResultFailed || report.Results[1].Status != doctorModelResultSkipped {
		t.Fatalf("results = %+v, want failed then skipped", report.Results)
	}
	if !strings.Contains(report.Results[1].Error, "cancellation") || strings.Contains(report.Results[1].Error, "--fail-fast") {
		t.Fatalf("skipped error = %q, want cancellation reason", report.Results[1].Error)
	}
}

func TestRunDoctorModelsReadsProjectModelPoolConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte("model_pools:\n  audit:\n    - local/gpt\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Chdir(projectRoot)

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{Pool: "audit", Timeout: 5 * time.Second, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Mode != doctorModelsModeModelPool || report.PoolName != "audit" {
		t.Fatalf("report mode/pool = %q/%q", report.Mode, report.PoolName)
	}
	if report.Summary.Passed != 1 || len(report.Results) != 1 || report.Results[0].CanonicalRef != "local/gpt" {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunDoctorModelsAppliesProjectProviderOverride(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		defer r.Body.Close()
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: http://127.0.0.1:1/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte("providers:\n  local:\n    api_url: "+server.URL+"/v1/chat/completions\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Chdir(projectRoot)

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "local/gpt", Timeout: 5 * time.Second, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Summary.Passed != 1 || len(report.Results) != 1 || report.Results[0].Status != doctorModelResultSuccess {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunDoctorModelsUsesOnlyCurrentWorkingDirectoryProjectConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	projectRoot := t.TempDir()
	nested := filepath.Join(projectRoot, "nested", "child")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir root .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte("model_pools:\n  root:\n    - local/gpt\n"), 0o600); err != nil {
		t.Fatalf("write root project config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(nested, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir nested .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".chord", "config.yaml"), []byte("model_pools:\n  nested:\n    - local/gpt\n"), 0o600); err != nil {
		t.Fatalf("write nested project config: %v", err)
	}
	t.Chdir(nested)

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{Pool: "nested", Timeout: 5 * time.Second, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels nested pool: %v\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode nested JSON report: %v\n%s", err, out.String())
	}
	if report.Mode != doctorModelsModeModelPool || report.PoolName != "nested" {
		t.Fatalf("report mode/pool = %q/%q", report.Mode, report.PoolName)
	}
	if report.Summary.Passed != 1 || len(report.Results) != 1 || report.Results[0].CanonicalRef != "local/gpt" {
		t.Fatalf("nested report = %+v", report)
	}

	out.Reset()
	err := runDoctorModels(t.Context(), doctorModelsOptions{Pool: "root", Timeout: 5 * time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("runDoctorModels root pool err = %v, want exit 2\noutput: %s", err, out.String())
	}
	var rootReport doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &rootReport); err != nil {
		t.Fatalf("decode root JSON report: %v\n%s", err, out.String())
	}
	if rootReport.Summary.ConfigErrors != 1 || len(rootReport.ConfigErrors) != 1 {
		t.Fatalf("root report = %+v", rootReport)
	}
	if !strings.Contains(rootReport.ConfigErrors[0].Error, "model_pool \"root\" not found in config") {
		t.Fatalf("root config error = %+v, want missing root pool message", rootReport.ConfigErrors[0])
	}
}

func TestRunDoctorModelsInvalidProjectConfigReturnsError(t *testing.T) {
	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "local:\n  - test-key\n")

	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte("providers:\n  local: [\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Chdir(projectRoot)

	var out bytes.Buffer
	err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "local/gpt", Timeout: time.Second, JSON: true, Out: &out})
	if err == nil {
		t.Fatal("runDoctorModels err = nil, want invalid project config error")
	}
	if !strings.Contains(err.Error(), "load project config") || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("runDoctorModels err = %v, want project parse error", err)
	}
}

func TestRunDoctorModelsUnsupportedProviderTypeIsConfigError(t *testing.T) {
	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  broken:\n"+
		"    type: not-a-provider\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "")

	var out bytes.Buffer
	err := runDoctorModels(t.Context(), doctorModelsOptions{Provider: "broken", Timeout: time.Second, JSON: true, Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("runDoctorModels err = %v, want exit 2\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.Summary.ConfigErrors != 1 || len(report.ConfigErrors) != 1 || !strings.Contains(report.ConfigErrors[0].Error, "invalid provider type") {
		t.Fatalf("report = %+v", report)
	}
}

func TestDoctorModelsOAuthRefresherUpdatesSharedRuntimeAuth(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	accessA := testUnsignedJWT(`{"chatgpt_account_id":"acc-a","chatgpt_user_id":"user-a"}`)
	accessB := testUnsignedJWT(`{"chatgpt_account_id":"acc-b","chatgpt_user_id":"user-b"}`)
	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  - refresh: refresh-a
    access: %s
    expires: 32503680000000
    account_id: acc-a
  - refresh: refresh-b
    access: %s
    expires: 32503680000000
    account_id: acc-b
`, accessA, accessB), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	runtimeCfg := &doctorModelsRuntimeConfig{Auth: auth, AuthPath: authPath}
	providerCfg := config.ProviderConfig{
		Type:     config.ProviderTypeResponses,
		Preset:   config.ProviderPresetCodex,
		APIURL:   config.OpenAICodexResponsesURL,
		TokenURL: config.OpenAIOAuthTokenURL,
		ClientID: config.OpenAIOAuthClientID,
		Models: map[string]config.ModelConfig{
			"gpt": {Limit: config.ModelLimit{Context: 1000, Output: 128}},
		},
	}
	creds := runtimeCfg.providerCredentials("openai")
	llmProviderCfg := llm.NewProviderConfig("openai", providerCfg, config.ExtractAPIKeys(creds))
	if err := configureDoctorModelsOAuthRefresher(runtimeCfg, "openai", providerCfg, creds, llmProviderCfg, ""); err != nil {
		t.Fatalf("configureDoctorModelsOAuthRefresher: %v", err)
	}

	llmProviderCfg.MarkExpired(accessA)

	runtimeCfg.AuthMu.Lock()
	gotFirst := runtimeCfg.Auth["openai"][0].OAuth.Status
	gotSecond := runtimeCfg.Auth["openai"][1].OAuth.Status
	runtimeCfg.AuthMu.Unlock()
	if gotFirst != config.OAuthStatusExpired {
		t.Fatalf("runtime auth first credential status = %q, want %q", gotFirst, config.OAuthStatusExpired)
	}
	if gotSecond != config.OAuthStatusNormal {
		t.Fatalf("runtime auth sibling credential status = %q, want normal", gotSecond)
	}

	credsAfter := runtimeCfg.providerCredentials("openai")
	if credsAfter[0].OAuth == nil || credsAfter[0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("next target credentials did not observe expired status: %#v", credsAfter[0].OAuth)
	}
}

func TestDoctorModelsOAuthRefresherSharesRefreshedTokenAcrossTargets(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	oldAccess := testUnsignedJWT(`{"chatgpt_account_id":"acc-a","chatgpt_user_id":"user-a"}`)
	siblingAccess := testUnsignedJWT(`{"chatgpt_account_id":"acc-b","chatgpt_user_id":"user-b"}`)
	newAccess := testUnsignedJWT(`{"chatgpt_account_id":"acc-a","chatgpt_user_id":"user-a"}`)
	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  - refresh: old-refresh
    access: %s
    expires: 32503680000000
    account_id: acc-a
  - refresh: sibling-refresh
    access: %s
    expires: 32503680000000
    account_id: acc-b
`, oldAccess, siblingAccess), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var refreshRequests atomic.Int32
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + newAccess + `","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer refreshServer.Close()

	runtimeCfg := &doctorModelsRuntimeConfig{Auth: auth, AuthPath: authPath}
	providerCfg := config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"gpt": {Limit: config.ModelLimit{Context: 1000, Output: 128}},
		},
	}
	creds := runtimeCfg.providerCredentials("openai")
	llmProviderCfg := llm.NewProviderConfig("openai", providerCfg, config.ExtractAPIKeys(creds))
	oauthMap, _, err := oauthCredentialMap(creds)
	if err != nil {
		t.Fatalf("oauthCredentialMap: %v", err)
	}
	llmProviderCfg.SetOAuthRefresher(refreshServer.URL, "client-id", runtimeCfg.AuthPath, "", &runtimeCfg.Auth, &runtimeCfg.AuthMu, oauthMap, "")

	refreshedKey, ok, err := llmProviderCfg.TryRefreshOAuthKey(context.Background(), oldAccess)
	if err != nil {
		t.Fatalf("TryRefreshOAuthKey: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for OAuth refresh")
	}
	if refreshedKey != newAccess {
		t.Fatalf("refreshedKey = %q, want refreshed access", refreshedKey)
	}
	if refreshRequests.Load() != 1 {
		t.Fatalf("refreshRequests = %d, want 1", refreshRequests.Load())
	}

	runtimeCfg.AuthMu.Lock()
	gotFirst := runtimeCfg.Auth["openai"][0].OAuth
	gotSecond := runtimeCfg.Auth["openai"][1].OAuth
	runtimeCfg.AuthMu.Unlock()
	if gotFirst == nil || gotFirst.Access != newAccess || gotFirst.Refresh != "new-refresh" {
		t.Fatalf("runtime auth first credential = %#v, want refreshed tokens", gotFirst)
	}
	if gotSecond == nil || gotSecond.Access != siblingAccess || gotSecond.Refresh != "sibling-refresh" {
		t.Fatalf("runtime auth sibling credential = %#v, want unchanged sibling", gotSecond)
	}

	credsAfter := runtimeCfg.providerCredentials("openai")
	if credsAfter[0].OAuth == nil || credsAfter[0].OAuth.Access != newAccess || credsAfter[0].OAuth.Refresh != "new-refresh" {
		t.Fatalf("next target credentials did not observe refreshed token: %#v", credsAfter[0].OAuth)
	}
	if credsAfter[1].OAuth == nil || credsAfter[1].OAuth.Access != siblingAccess {
		t.Fatalf("next target sibling credential changed unexpectedly: %#v", credsAfter[1].OAuth)
	}
}

func TestRunDoctorModelsNoAuthProviderStillRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		defer r.Body.Close()
		writeDoctorChatCompletionSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  local:\n"+
		"    type: chat-completions\n"+
		"    api_url: "+server.URL+"/v1/chat/completions\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "")

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "local/gpt", Timeout: 5 * time.Second, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestRunDoctorModelsUsesAccessTokenDespiteExpiredLocalExpires(t *testing.T) {
	var responseRequests atomic.Int32
	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseRequests.Add(1)
		defer r.Body.Close()
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeDoctorResponsesSSE(t, w)
	}))
	defer responseServer.Close()

	var refreshRequests atomic.Int32
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(-time.Hour).UnixMilli()
	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  openai:\n"+
		"    type: responses\n"+
		"    api_url: "+responseServer.URL+"/v1/responses\n"+
		"    token_url: "+refreshServer.URL+"\n"+
		"    client_id: client-id\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n",
		fmt.Sprintf("openai:\n  - access: access-token\n    expires: %d\n    account_id: acc-1\n", expires))

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "openai/gpt", Timeout: 5 * time.Second, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	if responseRequests.Load() != 1 {
		t.Fatalf("responseRequests = %d, want 1", responseRequests.Load())
	}
	if refreshRequests.Load() != 0 {
		t.Fatalf("refreshRequests = %d, want 0", refreshRequests.Load())
	}
}

func TestRunDoctorModelsResponsesReportsHTTPTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeDoctorResponsesSSE(t, w)
	}))
	defer server.Close()

	setupDoctorModelsConfigHome(t, "providers:\n"+
		"  openai:\n"+
		"    type: responses\n"+
		"    api_url: "+server.URL+"/v1/responses\n"+
		"    models:\n"+
		"      gpt:\n"+
		"        limit:\n"+
		"          context: 1000\n"+
		"          output: 128\n", "openai:\n  - test-key\n")

	var out bytes.Buffer
	if err := runDoctorModels(t.Context(), doctorModelsOptions{ModelRef: "openai/gpt", Timeout: 5 * time.Second, JSON: true, Out: &out}); err != nil {
		t.Fatalf("runDoctorModels: %v\noutput: %s", err, out.String())
	}
	var report doctorModelsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if len(report.Results) != 1 || report.Results[0].Transport != "http" {
		t.Fatalf("results = %+v", report.Results)
	}
}
