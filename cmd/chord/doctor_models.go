package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

const (
	doctorModelSourceProviderRepresentative = "provider-representative"
	doctorModelSourceExplicitModel          = "explicit-model"
	doctorModelSourceModelPool              = "model-pool"
	doctorModelSourceProviderAllModels      = "provider-all-models"

	doctorModelsModeProviderRepresentatives = "provider-representatives"
	doctorModelsModeExplicitModel           = "explicit-model"
	doctorModelsModeModelPool               = "model-pool"
	doctorModelsModeAllPools                = "all-pools"
	doctorModelsModeProviderAllModels       = "provider-all-models"

	doctorModelResultSuccess     = "success"
	doctorModelResultFailed      = "failed"
	doctorModelResultSkipped     = "skipped"
	doctorModelResultConfigError = "config_error"

	doctorModelsDefaultTimeout = 30 * time.Second
	doctorModelsDefaultRetry   = 1
)

type doctorModelsOptions struct {
	Provider  string
	ModelRef  string
	Pool      string
	AllModels bool
	AllPools  bool
	Timeout   time.Duration
	Retry     int
	FailFast  bool
	JSON      bool
	APIBase   string
	Out       io.Writer
}

type doctorModelsRuntimeConfig struct {
	Cfg       *config.Config
	Auth      config.AuthConfig
	AuthPath  string
	PoolOrder []string
}

type doctorModelTarget struct {
	Source       string
	PoolName     string
	PoolIndex    int // zero-based; -1 when not a pool entry
	ProviderName string
	ModelName    string
	VariantName  string
	CanonicalRef string
}

type doctorModelPlanEntry struct {
	Target *doctorModelTarget
	Result *doctorModelResult
}

type doctorModelsPlan struct {
	Mode      string
	Provider  string
	ModelRef  string
	PoolName  string
	PoolCount int
	Entries   []doctorModelPlanEntry
}

type doctorUsageResult struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

type doctorModelResult struct {
	Source         string             `json:"source"`
	PoolName       string             `json:"pool_name,omitempty"`
	PoolIndex      int                `json:"pool_index,omitempty"`
	ProviderName   string             `json:"provider,omitempty"`
	ModelName      string             `json:"model,omitempty"`
	VariantName    string             `json:"variant,omitempty"`
	CanonicalRef   string             `json:"ref,omitempty"`
	ProviderType   string             `json:"type,omitempty"`
	Transport      string             `json:"transport,omitempty"`
	Status         string             `json:"result"`
	LatencySeconds float64            `json:"latency_seconds,omitempty"`
	TextChunks     int                `json:"text_chunks,omitempty"`
	Usage          *doctorUsageResult `json:"usage,omitempty"`
	Error          string             `json:"error,omitempty"`
}

type doctorModelsSummary struct {
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Skipped      int `json:"skipped"`
	ConfigErrors int `json:"config_errors"`
}

type doctorModelFailure struct {
	Ref   string `json:"ref,omitempty"`
	Error string `json:"error"`
}

type doctorModelsReport struct {
	Mode          string               `json:"mode"`
	Provider      string               `json:"provider,omitempty"`
	ModelRef      string               `json:"model_ref,omitempty"`
	PoolName      string               `json:"pool_name,omitempty"`
	Results       []doctorModelResult  `json:"results"`
	Summary       doctorModelsSummary  `json:"summary"`
	FailedModels  []doctorModelFailure `json:"failed_models,omitempty"`
	ConfigErrors  []doctorModelFailure `json:"config_errors,omitempty"`
	Canceled      bool                 `json:"canceled,omitempty"`
	ExecutionTime float64              `json:"execution_time_seconds,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run configuration and runtime diagnostics",
	}
	cmd.AddCommand(newDoctorModelsCmd())
	return cmd
}

func newDoctorModelsCmd() *cobra.Command {
	opts := doctorModelsOptions{Timeout: doctorModelsDefaultTimeout, Retry: doctorModelsDefaultRetry}
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Diagnose configured model calls",
		Long: `Run lightweight model configuration smoke tests using the real provider transport path.

The canonical model reference is provider/model[@variant]. Bare model references
are only accepted when --provider is specified. Model-pool checks test each pool
entry independently and do not use fallback, so a bad fallback target is not
hidden by a later successful model.

By default, diagnostics make a single request attempt per target. Increase --retry
only when you explicitly want extra retry rounds for transient failures.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("retry") && opts.Retry <= 0 {
				return cliExitError{code: 2, err: fmt.Errorf("--retry must be greater than zero")}
			}
			opts.Out = cmd.OutOrStdout()
			opts.APIBase = strings.TrimSpace(flagAPIBase)
			return runDoctorModels(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Provider, "provider", "", "Provider name to test")
	cmd.Flags().StringVar(&opts.ModelRef, "model", "", "Model reference to test (provider/model[@variant], or model[@variant] with --provider)")
	cmd.Flags().StringVar(&opts.Pool, "pool", "", "Model pool name to test")
	cmd.Flags().BoolVar(&opts.AllModels, "all-models", false, "Test all configured models for --provider")
	cmd.Flags().BoolVar(&opts.AllPools, "all-pools", false, "Test all configured model pools")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", doctorModelsDefaultTimeout, "Per-model request timeout")
	cmd.Flags().IntVar(&opts.Retry, "retry", doctorModelsDefaultRetry, "Maximum request attempts per target (default 1)")
	cmd.Flags().BoolVar(&opts.FailFast, "fail-fast", false, "Stop after the first failed request or configuration error")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	return cmd
}

func runDoctorModels(parentCtx context.Context, opts doctorModelsOptions) error {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	opts = normalizeDoctorModelsOptions(opts)
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	if err := validateDoctorModelsOptions(opts); err != nil {
		return cliExitError{code: 2, err: err}
	}

	runtimeCfg, err := loadDoctorModelsRuntimeConfig()
	if err != nil {
		return cliExitError{code: 2, err: err}
	}
	plan, err := buildDoctorModelsPlan(runtimeCfg.Cfg, runtimeCfg.PoolOrder, opts)
	if err != nil {
		return cliExitError{code: 2, err: err}
	}

	started := time.Now()
	renderEach := func(int, int, doctorModelResult) {}
	if !opts.JSON {
		fmt.Fprintln(out, doctorModelsPlanHeader(plan))
		fmt.Fprintln(out)
		renderEach = func(idx, total int, result doctorModelResult) {
			renderDoctorModelResult(out, plan, idx, total, result)
		}
	}
	report := executeDoctorModelsPlan(parentCtx, runtimeCfg, plan, opts, renderEach)
	report.ExecutionTime = time.Since(started).Seconds()
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return cliExitError{code: 2, err: fmt.Errorf("write JSON report: %w", err)}
		}
	} else {
		renderDoctorModelsSummary(out, report)
	}

	if report.Canceled {
		return cliExitError{code: 130, err: context.Canceled}
	}
	if report.Summary.ConfigErrors > 0 {
		return cliExitError{code: 2, err: fmt.Errorf("%d model configuration error(s)", report.Summary.ConfigErrors)}
	}
	if report.Summary.Failed > 0 {
		return cliExitError{code: 1, err: fmt.Errorf("%d model diagnostic(s) failed", report.Summary.Failed)}
	}
	return nil
}

func normalizeDoctorModelsOptions(opts doctorModelsOptions) doctorModelsOptions {
	if opts.Retry == 0 {
		opts.Retry = doctorModelsDefaultRetry
	}
	return opts
}

func validateDoctorModelsOptions(opts doctorModelsOptions) error {
	provider := strings.TrimSpace(opts.Provider)
	modelRef := strings.TrimSpace(opts.ModelRef)
	pool := strings.TrimSpace(opts.Pool)
	selected := 0
	if modelRef != "" {
		selected++
	}
	if pool != "" {
		selected++
	}
	if opts.AllPools {
		selected++
	}
	if selected > 1 {
		return fmt.Errorf("--model, --pool, and --all-pools are mutually exclusive")
	}
	if pool != "" && provider != "" {
		return fmt.Errorf("--pool cannot be combined with --provider")
	}
	if opts.AllPools && provider != "" {
		return fmt.Errorf("--all-pools cannot be combined with --provider")
	}
	if opts.AllModels {
		if provider == "" {
			return fmt.Errorf("--all-models requires --provider")
		}
		if modelRef != "" || pool != "" || opts.AllPools {
			return fmt.Errorf("--all-models cannot be combined with --model, --pool, or --all-pools")
		}
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("--timeout must be greater than zero")
	}
	if opts.Retry < 0 {
		return fmt.Errorf("--retry must be greater than zero")
	}
	return nil
}

func applyDoctorModelsAPIBase(providerCfg config.ProviderConfig, apiBase string) config.ProviderConfig {
	apiBase = strings.TrimSpace(apiBase)
	if apiBase != "" {
		providerCfg.APIURL = apiBase
	}
	return providerCfg
}

type orderedModelPool struct {
	Name string
	Refs []string
}

func loadDoctorModelsRuntimeConfig() (*doctorModelsRuntimeConfig, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	poolOrder := []string(nil)
	if cfgPath, pathErr := config.ConfigPath(); pathErr == nil {
		if pools, orderErr := orderedModelPoolsFromPath(cfgPath); orderErr == nil {
			poolOrder = appendPoolOrder(poolOrder, pools)
		} else {
			return nil, fmt.Errorf("read model_pools order: %w", orderErr)
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		projectConfigPath := config.ProjectConfigPath(cwd)
		projectCfg, mergedCfg, mergeErr := config.MergeProjectConfig(cfg, projectConfigPath)
		if mergeErr != nil {
			return nil, fmt.Errorf("load project config: %w", mergeErr)
		}
		if projectCfg != nil {
			cfg = mergedCfg
			if pools, orderErr := orderedModelPoolsFromPath(projectConfigPath); orderErr == nil {
				poolOrder = appendPoolOrder(poolOrder, pools)
			} else {
				return nil, fmt.Errorf("read model_pools order: %w", orderErr)
			}
		}
	}
	poolOrder = completeModelPoolOrder(poolOrder, cfg.ModelPools)

	authPath, err := config.AuthPath()
	if err != nil {
		return nil, fmt.Errorf("get auth path: %w", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		return nil, fmt.Errorf("load auth: %w", err)
	}
	return &doctorModelsRuntimeConfig{Cfg: cfg, Auth: auth, AuthPath: authPath, PoolOrder: poolOrder}, nil
}

func orderedModelPoolsFromPath(path string) ([]orderedModelPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		value := root.Content[i+1]
		if key.Value != "model_pools" {
			continue
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: model_pools must be a mapping", path)
		}
		pools := make([]orderedModelPool, 0, len(value.Content)/2)
		for j := 0; j+1 < len(value.Content); j += 2 {
			poolName := strings.TrimSpace(value.Content[j].Value)
			var refs []string
			if err := value.Content[j+1].Decode(&refs); err != nil {
				return nil, fmt.Errorf("%s: decode model_pools.%s: %w", path, poolName, err)
			}
			pools = append(pools, orderedModelPool{Name: poolName, Refs: refs})
		}
		return pools, nil
	}
	return nil, nil
}

func appendPoolOrder(order []string, pools []orderedModelPool) []string {
	seen := make(map[string]struct{}, len(order)+len(pools))
	out := make([]string, 0, len(order)+len(pools))
	for _, name := range order {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, pool := range pools {
		name := strings.TrimSpace(pool.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func completeModelPoolOrder(order []string, pools map[string][]string) []string {
	seen := make(map[string]struct{}, len(order)+len(pools))
	out := make([]string, 0, len(order)+len(pools))
	for _, name := range order {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := pools[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	missing := make([]string, 0, len(pools))
	for name := range pools {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if _, ok := seen[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	out = append(out, missing...)
	return out
}

func buildDoctorModelsPlan(cfg *config.Config, poolOrder []string, opts doctorModelsOptions) (doctorModelsPlan, error) {
	provider := strings.TrimSpace(opts.Provider)
	modelRef := strings.TrimSpace(opts.ModelRef)
	poolName := strings.TrimSpace(opts.Pool)
	plan := doctorModelsPlan{Provider: provider, ModelRef: modelRef, PoolName: poolName}
	if cfg == nil {
		return plan, fmt.Errorf("config is nil")
	}

	switch {
	case modelRef != "":
		plan.Mode = doctorModelsModeExplicitModel
		entry, entryErr := explicitModelPlanEntry(cfg, provider, modelRef)
		if entryErr != nil {
			return plan, entryErr
		}
		if entry.Result == nil && entry.Target != nil {
			plan.ModelRef = entry.Target.CanonicalRef
		}
		plan.Entries = append(plan.Entries, entry)
	case poolName != "":
		plan.Mode = doctorModelsModeModelPool
		plan.Entries = append(plan.Entries, modelPoolPlanEntries(cfg, poolName)...)
	case opts.AllPools:
		plan.Mode = doctorModelsModeAllPools
		plan.PoolCount = len(poolOrder)
		if len(poolOrder) == 0 {
			plan.Entries = append(plan.Entries, resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceModelPool, CanonicalRef: "model_pools"}, "no model_pools configured")))
			break
		}
		for _, name := range poolOrder {
			plan.Entries = append(plan.Entries, modelPoolPlanEntries(cfg, name)...)
		}
	case opts.AllModels:
		plan.Mode = doctorModelsModeProviderAllModels
		plan.Entries = append(plan.Entries, providerAllModelsPlanEntries(cfg, provider)...)
	default:
		plan.Mode = doctorModelsModeProviderRepresentatives
		plan.Entries = append(plan.Entries, providerRepresentativePlanEntries(cfg, poolOrder, provider)...)
	}

	return plan, nil
}

func explicitModelPlanEntry(cfg *config.Config, providerFlag, rawRef string) (doctorModelPlanEntry, error) {
	provider, model, variant, err := parseExplicitDoctorModelRef(cfg, providerFlag, rawRef)
	if err != nil {
		return doctorModelPlanEntry{}, err
	}
	target := doctorModelTarget{
		Source:       doctorModelSourceExplicitModel,
		PoolIndex:    -1,
		ProviderName: provider,
		ModelName:    model,
		VariantName:  variant,
		CanonicalRef: canonicalDoctorModelRef(provider, model, variant),
	}
	return validatedTargetPlanEntry(cfg, target), nil
}

func parseExplicitDoctorModelRef(cfg *config.Config, providerFlag, rawRef string) (provider, model, variant string, err error) {
	base, variant := parseDoctorModelRef(rawRef)
	if base == "" {
		return "", "", "", fmt.Errorf("model reference must not be empty")
	}
	providerFlag = strings.TrimSpace(providerFlag)
	if providerFlag != "" {
		if providerCfg, ok := cfg.Providers[providerFlag]; ok {
			if _, ok := providerCfg.Models[base]; ok {
				return providerFlag, base, variant, nil
			}
		}
		if strings.Contains(base, "/") {
			refProvider, refModel := splitProviderModelRef(base)
			if refProvider != providerFlag {
				return refProvider, refModel, variant, fmt.Errorf("--provider %q does not match model reference provider %q", providerFlag, refProvider)
			}
			return refProvider, refModel, variant, nil
		}
		return providerFlag, base, variant, nil
	}
	if !strings.Contains(base, "/") {
		return "", base, variant, fmt.Errorf("model reference %q does not include a provider.\n\nUse a full model reference:\n  chord doctor models --model openai/%s\n\nOr specify the provider separately:\n  chord doctor models --provider openai --model %s", strings.TrimSpace(rawRef), strings.TrimSpace(rawRef), strings.TrimSpace(rawRef))
	}
	provider, model = splitProviderModelRef(base)
	return provider, model, variant, nil
}

func providerRepresentativePlanEntries(cfg *config.Config, poolOrder []string, providerFilter string) []doctorModelPlanEntry {
	if providerFilter != "" {
		if _, ok := cfg.Providers[providerFilter]; !ok {
			return []doctorModelPlanEntry{resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceProviderRepresentative, ProviderName: providerFilter, CanonicalRef: providerFilter}, fmt.Sprintf("provider %q not found in config", providerFilter)))}
		}
		entry := representativePlanEntryForProvider(cfg, poolOrder, providerFilter)
		return []doctorModelPlanEntry{entry}
	}
	providerNames := sortedProviderNames(cfg.Providers)
	if len(providerNames) == 0 {
		target := doctorModelTarget{Source: doctorModelSourceProviderRepresentative, CanonicalRef: "providers"}
		return []doctorModelPlanEntry{resultPlanEntry(configErrorResult(target, "no providers configured"))}
	}
	entries := make([]doctorModelPlanEntry, 0, len(providerNames))
	for _, name := range providerNames {
		entries = append(entries, representativePlanEntryForProvider(cfg, poolOrder, name))
	}
	return entries
}

func representativePlanEntryForProvider(cfg *config.Config, poolOrder []string, providerName string) doctorModelPlanEntry {
	for _, poolName := range poolOrder {
		for _, rawRef := range cfg.ModelPools[poolName] {
			base, variant := parseDoctorModelRef(rawRef)
			if !strings.Contains(base, "/") {
				continue
			}
			refProvider, refModel := splitProviderModelRef(base)
			if refProvider != providerName {
				continue
			}
			target := doctorModelTarget{
				Source:       doctorModelSourceProviderRepresentative,
				PoolIndex:    -1,
				ProviderName: refProvider,
				ModelName:    refModel,
				VariantName:  variant,
				CanonicalRef: canonicalDoctorModelRef(refProvider, refModel, variant),
			}
			return validatedTargetPlanEntry(cfg, target)
		}
	}

	providerCfg := cfg.Providers[providerName]
	modelNames := sortedModelNames(providerCfg.Models)
	if len(modelNames) == 0 {
		return resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceProviderRepresentative, ProviderName: providerName, CanonicalRef: providerName}, fmt.Sprintf("provider %q has no models configured", providerName)))
	}
	target := doctorModelTarget{
		Source:       doctorModelSourceProviderRepresentative,
		PoolIndex:    -1,
		ProviderName: providerName,
		ModelName:    modelNames[0],
		CanonicalRef: canonicalDoctorModelRef(providerName, modelNames[0], ""),
	}
	return validatedTargetPlanEntry(cfg, target)
}

func providerAllModelsPlanEntries(cfg *config.Config, providerName string) []doctorModelPlanEntry {
	providerCfg, ok := cfg.Providers[providerName]
	if !ok {
		return []doctorModelPlanEntry{resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceProviderAllModels, ProviderName: providerName, CanonicalRef: providerName}, fmt.Sprintf("provider %q not found in config", providerName)))}
	}
	modelNames := sortedModelNames(providerCfg.Models)
	if len(modelNames) == 0 {
		return []doctorModelPlanEntry{resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceProviderAllModels, ProviderName: providerName, CanonicalRef: providerName}, fmt.Sprintf("provider %q has no models configured", providerName)))}
	}
	entries := make([]doctorModelPlanEntry, 0, len(modelNames))
	for _, modelName := range modelNames {
		target := doctorModelTarget{
			Source:       doctorModelSourceProviderAllModels,
			PoolIndex:    -1,
			ProviderName: providerName,
			ModelName:    modelName,
			CanonicalRef: canonicalDoctorModelRef(providerName, modelName, ""),
		}
		entries = append(entries, validatedTargetPlanEntry(cfg, target))
	}
	return entries
}

func modelPoolPlanEntries(cfg *config.Config, poolName string) []doctorModelPlanEntry {
	refs, ok := cfg.ModelPools[poolName]
	if !ok {
		return []doctorModelPlanEntry{resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceModelPool, PoolName: poolName, PoolIndex: -1, CanonicalRef: poolName}, fmt.Sprintf("model_pool %q not found in config", poolName)))}
	}
	if len(refs) == 0 {
		return []doctorModelPlanEntry{resultPlanEntry(configErrorResult(doctorModelTarget{Source: doctorModelSourceModelPool, PoolName: poolName, PoolIndex: -1, CanonicalRef: poolName}, fmt.Sprintf("model_pool %q is empty", poolName)))}
	}
	entries := make([]doctorModelPlanEntry, 0, len(refs))
	for i, rawRef := range refs {
		entries = append(entries, modelPoolRefPlanEntry(cfg, poolName, i, rawRef))
	}
	return entries
}

func modelPoolRefPlanEntry(cfg *config.Config, poolName string, poolIndex int, rawRef string) doctorModelPlanEntry {
	base, variant := parseDoctorModelRef(rawRef)
	target := doctorModelTarget{Source: doctorModelSourceModelPool, PoolName: poolName, PoolIndex: poolIndex, CanonicalRef: strings.TrimSpace(rawRef)}
	if base == "" {
		return resultPlanEntry(configErrorResult(target, fmt.Sprintf("model_pool %q entry %d is empty", poolName, poolIndex+1)))
	}
	if !strings.Contains(base, "/") {
		return resultPlanEntry(configErrorResult(target, fmt.Sprintf("model_pool %q entry %d model reference %q does not include a provider; use provider/model[@variant]", poolName, poolIndex+1, strings.TrimSpace(rawRef))))
	}
	provider, model := splitProviderModelRef(base)
	target.ProviderName = provider
	target.ModelName = model
	target.VariantName = variant
	target.CanonicalRef = canonicalDoctorModelRef(provider, model, variant)
	return validatedTargetPlanEntry(cfg, target)
}

func validatedTargetPlanEntry(cfg *config.Config, target doctorModelTarget) doctorModelPlanEntry {
	if target.PoolIndex == 0 && target.PoolName == "" {
		target.PoolIndex = -1
	}
	if err := validateDoctorModelTarget(cfg, target); err != nil {
		return resultPlanEntry(configErrorResult(target, err.Error()))
	}
	targetCopy := target
	return doctorModelPlanEntry{Target: &targetCopy}
}

func resultPlanEntry(result doctorModelResult) doctorModelPlanEntry {
	resultCopy := result
	return doctorModelPlanEntry{Result: &resultCopy}
}

func validateDoctorModelTarget(cfg *config.Config, target doctorModelTarget) error {
	if strings.TrimSpace(target.ProviderName) == "" {
		return fmt.Errorf("provider is empty for model reference %q", target.CanonicalRef)
	}
	providerCfg, ok := cfg.Providers[target.ProviderName]
	if !ok {
		return fmt.Errorf("provider %q not found in config", target.ProviderName)
	}
	if strings.TrimSpace(target.ModelName) == "" {
		return fmt.Errorf("model is empty for provider %q", target.ProviderName)
	}
	modelCfg, ok := providerCfg.Models[target.ModelName]
	if !ok {
		return fmt.Errorf("model %q not found in provider %q", target.ModelName, target.ProviderName)
	}
	if target.VariantName != "" {
		if _, ok := modelCfg.Variants[target.VariantName]; !ok {
			return fmt.Errorf("variant %q not found for model %q in provider %q", target.VariantName, target.ModelName, target.ProviderName)
		}
	}
	return nil
}

func executeDoctorModelsPlan(parentCtx context.Context, runtimeCfg *doctorModelsRuntimeConfig, plan doctorModelsPlan, opts doctorModelsOptions, renderEach func(int, int, doctorModelResult)) doctorModelsReport {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	report := doctorModelsReport{Mode: plan.Mode, Provider: plan.Provider, ModelRef: plan.ModelRef, PoolName: plan.PoolName}
	total := len(plan.Entries)
	for i, entry := range plan.Entries {
		if report.Canceled {
			for _, skipped := range plan.Entries[i:] {
				if skipped.Target == nil && skipped.Result == nil {
					continue
				}
				skippedResult := skippedDoctorModelResult(skipped)
				report.Results = append(report.Results, skippedResult)
				accumulateDoctorModelsSummary(&report, skippedResult)
			}
			break
		}
		var result doctorModelResult
		if entry.Result != nil {
			result = *entry.Result
		} else if entry.Target != nil {
			result = executeDoctorModelTarget(parentCtx, runtimeCfg, *entry.Target, opts)
		} else {
			result = doctorModelResult{Status: doctorModelResultSkipped, Error: "empty diagnostic target"}
		}
		if isDoctorModelCanceled(parentCtx, result) {
			report.Canceled = true
		}
		report.Results = append(report.Results, result)
		accumulateDoctorModelsSummary(&report, result)
		if renderEach != nil {
			renderEach(i+1, total, result)
		}
		if opts.FailFast && result.Status != doctorModelResultSuccess {
			for _, skipped := range plan.Entries[i+1:] {
				if skipped.Target == nil && skipped.Result == nil {
					continue
				}
				skippedResult := skippedDoctorModelResult(skipped)
				report.Results = append(report.Results, skippedResult)
				accumulateDoctorModelsSummary(&report, skippedResult)
			}
			break
		}
	}
	return report
}

func executeDoctorModelTarget(parentCtx context.Context, runtimeCfg *doctorModelsRuntimeConfig, target doctorModelTarget, opts doctorModelsOptions) doctorModelResult {
	result := resultFromTarget(target)
	if runtimeCfg == nil || runtimeCfg.Cfg == nil {
		result.Status = doctorModelResultConfigError
		result.Error = "config is nil"
		return result
	}
	cfg := runtimeCfg.Cfg
	providerCfg, ok := cfg.Providers[target.ProviderName]
	if !ok {
		result.Status = doctorModelResultConfigError
		result.Error = fmt.Sprintf("provider %q not found in config", target.ProviderName)
		return result
	}
	modelCfg, ok := providerCfg.Models[target.ModelName]
	if !ok {
		result.Status = doctorModelResultConfigError
		result.Error = fmt.Sprintf("model %q not found in provider %q", target.ModelName, target.ProviderName)
		return result
	}
	if target.VariantName != "" {
		if _, ok := modelCfg.Variants[target.VariantName]; !ok {
			result.Status = doctorModelResultConfigError
			result.Error = fmt.Sprintf("variant %q not found for model %q in provider %q", target.VariantName, target.ModelName, target.ProviderName)
			return result
		}
	}

	creds := runtimeCfg.Auth[target.ProviderName]
	providerCfg = applyDoctorModelsAPIBase(providerCfg, opts.APIBase)
	normalizedCfg, err := normalizeProviderConfig(target.ProviderName, providerCfg, creds)
	if err != nil {
		result.Status = doctorModelResultConfigError
		result.Error = err.Error()
		return result
	}
	result.ProviderType = normalizedCfg.Type
	modelCfg = normalizedCfg.Models[target.ModelName]
	apiKeys := config.ExtractAPIKeys(creds)
	if len(apiKeys) > 1 {
		apiKeys = apiKeys[:1]
	}
	llmProviderCfg := llm.NewProviderConfig(target.ProviderName, normalizedCfg, apiKeys)
	defer llmProviderCfg.StopCodexRateLimitPolling()
	if normalizedCfg.RateLimit > 0 {
		llmProviderCfg.SetRateLimiter(normalizedCfg.RateLimit)
	}
	effectiveProxy := llm.ResolveEffectiveProxy(normalizedCfg.Proxy, cfg.Proxy)
	if tokenURL, clientID, ok, err := resolveProviderOAuthSettings(normalizedCfg, creds); err != nil {
		result.Status = doctorModelResultConfigError
		result.Error = fmt.Sprintf("resolve OAuth settings for provider %q: %v", target.ProviderName, err)
		return result
	} else if ok {
		authCopy := runtimeCfg.Auth
		var authMu sync.Mutex
		oauthMap, backfills := oauthCredentialMap(creds)
		llmProviderCfg.SetOAuthRefresher(tokenURL, clientID, runtimeCfg.AuthPath, &authCopy, &authMu, oauthMap, effectiveProxy)
		if len(backfills) > 0 {
			if saveErr := persistOAuthMetadataBackfills(runtimeCfg.AuthPath, &authCopy, &authMu, target.ProviderName, backfills); saveErr != nil {
				log.Warnf("failed to persist backfilled OAuth email/account_id provider=%v error=%v", target.ProviderName, saveErr)
			}
		}
	}

	impl, err := newDoctorModelProvider(normalizedCfg.Type, llmProviderCfg, effectiveProxy, target.ProviderName)
	if err != nil {
		result.Status = doctorModelResultConfigError
		result.Error = err.Error()
		return result
	}
	client := llm.NewClient(llmProviderCfg, impl, target.ModelName, modelCfg.Limit.Output, "You are a helpful assistant.")
	client.SetOutputTokenMax(cfg.MaxOutputTokens)
	client.SetStreamRetryRounds(opts.Retry)
	client.SetTerminalAPIStatusCodes(400, 401, 403)
	if target.VariantName != "" {
		client.SetVariant(target.VariantName)
	}

	reqCtx, cancel := context.WithTimeout(parentCtx, opts.Timeout)
	defer cancel()
	messages := []message.Message{{Role: "user", Content: "Say hello in one word."}}
	textChunks := 0
	start := time.Now()
	resp, err := client.CompleteStream(reqCtx, messages, nil, func(delta message.StreamDelta) {
		if delta.Type == "text" {
			textChunks++
		}
	})
	result.LatencySeconds = time.Since(start).Seconds()
	if transportProvider, ok := impl.(interface{ LastTransportUsed() string }); ok {
		result.Transport = transportProvider.LastTransportUsed()
	}
	result.TextChunks = textChunks
	if err != nil {
		result.Status = doctorModelResultFailed
		result.Error = err.Error()
		return result
	}
	result.Status = doctorModelResultSuccess
	if resp != nil && resp.Usage != nil {
		result.Usage = &doctorUsageResult{Input: resp.Usage.InputTokens, Output: resp.Usage.OutputTokens}
	}
	return result
}

func newDoctorModelProvider(providerType string, providerCfg *llm.ProviderConfig, proxyURL, providerName string) (llm.Provider, error) {
	switch providerType {
	case config.ProviderTypeMessages:
		p, err := llm.NewAnthropicProvider(providerCfg, proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeMessages, providerName, err)
		}
		return p, nil
	case config.ProviderTypeChatCompletions:
		p, err := llm.NewOpenAIProvider(providerCfg, proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeChatCompletions, providerName, err)
		}
		return p, nil
	case config.ProviderTypeResponses:
		p, err := llm.NewResponsesProvider(providerCfg, proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeResponses, providerName, err)
		}
		return p, nil
	case config.ProviderTypeGenerateContent:
		p, err := llm.NewGeminiProvider(providerCfg, proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeGenerateContent, providerName, err)
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q for %q (allowed: %s, %s, %s, %s)", providerType, providerName, config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent)
	}
}

func configErrorResult(target doctorModelTarget, message string) doctorModelResult {
	result := resultFromTarget(target)
	result.Status = doctorModelResultConfigError
	result.Error = message
	return result
}

func resultFromTarget(target doctorModelTarget) doctorModelResult {
	poolIndex := 0
	if target.PoolName != "" && target.PoolIndex >= 0 {
		poolIndex = target.PoolIndex + 1
	}
	return doctorModelResult{
		Source:       target.Source,
		PoolName:     target.PoolName,
		PoolIndex:    poolIndex,
		ProviderName: target.ProviderName,
		ModelName:    target.ModelName,
		VariantName:  target.VariantName,
		CanonicalRef: target.CanonicalRef,
	}
}

func skippedDoctorModelResult(entry doctorModelPlanEntry) doctorModelResult {
	var result doctorModelResult
	if entry.Target != nil {
		result = resultFromTarget(*entry.Target)
	} else if entry.Result != nil {
		result = *entry.Result
	} else {
		result = doctorModelResult{}
	}
	result.Status = doctorModelResultSkipped
	result.Error = "skipped due to --fail-fast"
	return result
}

func accumulateDoctorModelsSummary(report *doctorModelsReport, result doctorModelResult) {
	if report == nil {
		return
	}
	switch result.Status {
	case doctorModelResultSuccess:
		report.Summary.Passed++
	case doctorModelResultFailed:
		report.Summary.Failed++
		report.FailedModels = append(report.FailedModels, doctorModelFailure{Ref: displayDoctorModelRef(result), Error: result.Error})
	case doctorModelResultSkipped:
		report.Summary.Skipped++
	case doctorModelResultConfigError:
		report.Summary.ConfigErrors++
		report.ConfigErrors = append(report.ConfigErrors, doctorModelFailure{Ref: displayDoctorModelRef(result), Error: result.Error})
	}
}

func isDoctorModelCanceled(parentCtx context.Context, result doctorModelResult) bool {
	if parentCtx != nil && errors.Is(parentCtx.Err(), context.Canceled) {
		return true
	}
	return result.Status == doctorModelResultFailed && strings.Contains(strings.ToLower(result.Error), "context canceled")
}

func doctorModelsPlanHeader(plan doctorModelsPlan) string {
	switch plan.Mode {
	case doctorModelsModeExplicitModel:
		ref := plan.ModelRef
		if ref == "" && len(plan.Entries) > 0 {
			ref = displayPlanEntryRef(plan.Entries[0])
		}
		return fmt.Sprintf("Checking model: %s", ref)
	case doctorModelsModeModelPool:
		return fmt.Sprintf("Checking model pool: %s (%d models)", plan.PoolName, len(plan.Entries))
	case doctorModelsModeAllPools:
		return fmt.Sprintf("Checking all model pools (%d pools, %d models)", plan.PoolCount, len(plan.Entries))
	case doctorModelsModeProviderAllModels:
		return fmt.Sprintf("Checking provider %q models (%d models)", plan.Provider, len(plan.Entries))
	default:
		if plan.Provider != "" {
			if len(plan.Entries) > 0 {
				return fmt.Sprintf("Checking provider %q using representative model %s", plan.Provider, displayPlanEntryRef(plan.Entries[0]))
			}
			return fmt.Sprintf("Checking provider %q", plan.Provider)
		}
		return "Checking configured model providers..."
	}
}

func renderDoctorModelResult(out io.Writer, plan doctorModelsPlan, idx, total int, result doctorModelResult) {
	numbered := total > 1 || plan.Mode != doctorModelsModeExplicitModel
	indent := ""
	if numbered {
		fmt.Fprintf(out, "[%d/%d] %s\n", idx, total, displayDoctorModelRef(result))
		indent = "  "
	}
	if result.PoolName != "" && plan.Mode == doctorModelsModeAllPools {
		fmt.Fprintf(out, "%spool: %s", indent, result.PoolName)
		if result.PoolIndex > 0 {
			fmt.Fprintf(out, "[%d]", result.PoolIndex)
		}
		fmt.Fprintln(out)
	}
	if result.ProviderName != "" {
		fmt.Fprintf(out, "%sprovider: %s\n", indent, result.ProviderName)
	}
	if result.ModelName != "" && !numbered {
		fmt.Fprintf(out, "%smodel: %s\n", indent, result.ModelName)
	}
	if result.VariantName != "" {
		fmt.Fprintf(out, "%svariant: %s\n", indent, result.VariantName)
	}
	if result.ProviderType != "" {
		fmt.Fprintf(out, "%stype: %s\n", indent, result.ProviderType)
	}
	if result.Transport != "" {
		fmt.Fprintf(out, "%stransport: %s\n", indent, result.Transport)
	}
	fmt.Fprintf(out, "%sresult: %s\n", indent, humanDoctorModelStatus(result.Status))
	if result.LatencySeconds > 0 {
		fmt.Fprintf(out, "%slatency: %.2fs\n", indent, result.LatencySeconds)
	}
	if result.TextChunks > 0 || result.Status == doctorModelResultSuccess {
		fmt.Fprintf(out, "%stext chunks: %d\n", indent, result.TextChunks)
	}
	if result.Usage != nil {
		fmt.Fprintf(out, "%susage: input=%d output=%d\n", indent, result.Usage.Input, result.Usage.Output)
	}
	if result.Error != "" {
		fmt.Fprintf(out, "%serror: %s\n", indent, result.Error)
	}
	fmt.Fprintln(out)
}

func renderDoctorModelsSummary(out io.Writer, report doctorModelsReport) {
	fmt.Fprintf(out, "Summary: %d passed, %d failed, %d skipped", report.Summary.Passed, report.Summary.Failed, report.Summary.Skipped)
	if report.Summary.ConfigErrors > 0 {
		fmt.Fprintf(out, ", %d config errors", report.Summary.ConfigErrors)
	}
	fmt.Fprintln(out)
	if len(report.FailedModels) > 0 {
		fmt.Fprintln(out, "Failed models:")
		for _, failure := range report.FailedModels {
			fmt.Fprintf(out, "  - %s: %s\n", failure.Ref, failure.Error)
		}
	}
	if len(report.ConfigErrors) > 0 {
		fmt.Fprintln(out, "Configuration errors:")
		for _, failure := range report.ConfigErrors {
			fmt.Fprintf(out, "  - %s: %s\n", failure.Ref, failure.Error)
		}
	}
}

func humanDoctorModelStatus(status string) string {
	switch status {
	case doctorModelResultConfigError:
		return "config error"
	default:
		return status
	}
}

func displayPlanEntryRef(entry doctorModelPlanEntry) string {
	if entry.Target != nil {
		return entry.Target.CanonicalRef
	}
	if entry.Result != nil {
		return displayDoctorModelRef(*entry.Result)
	}
	return ""
}

func displayDoctorModelRef(result doctorModelResult) string {
	if result.CanonicalRef != "" {
		return result.CanonicalRef
	}
	if result.ProviderName != "" && result.ModelName != "" {
		return canonicalDoctorModelRef(result.ProviderName, result.ModelName, result.VariantName)
	}
	if result.ProviderName != "" {
		return result.ProviderName
	}
	if result.PoolName != "" {
		return result.PoolName
	}
	return "unknown"
}

func newTestProvidersCmd() *cobra.Command {
	var providerFilter string
	cmd := &cobra.Command{
		Use:        "test-providers",
		Short:      "Deprecated alias for doctor models",
		Deprecated: "use 'chord doctor models' instead",
		Hidden:     true,
		Args:       cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctorModels(cmd.Context(), doctorModelsOptions{
				Provider: strings.TrimSpace(providerFilter),
				Timeout:  doctorModelsDefaultTimeout,
				Retry:    doctorModelsDefaultRetry,
				Out:      cmd.OutOrStdout(),
				APIBase:  strings.TrimSpace(flagAPIBase),
			})
		},
	}
	cmd.Flags().StringVar(&providerFilter, "provider", "", "Provider name to test")
	return cmd
}

func parseDoctorModelRef(raw string) (base, variant string) {
	base, variant = config.ParseModelRef(strings.TrimSpace(raw))
	return strings.TrimSpace(base), strings.TrimSpace(variant)
}

func splitProviderModelRef(base string) (provider, model string) {
	parts := strings.SplitN(strings.TrimSpace(base), "/", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(base), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func canonicalDoctorModelRef(provider, model, variant string) string {
	ref := strings.TrimSpace(provider) + "/" + strings.TrimSpace(model)
	if strings.TrimSpace(variant) != "" {
		ref += "@" + strings.TrimSpace(variant)
	}
	return ref
}

func sortedProviderNames(providers map[string]config.ProviderConfig) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedModelNames(models map[string]config.ModelConfig) []string {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
