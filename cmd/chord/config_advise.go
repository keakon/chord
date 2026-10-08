package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
)

type configAdviseOptions struct {
	JSON   bool
	Accept string
	Keep   bool
}

func newConfigAdviseCmd() *cobra.Command {
	opts := &configAdviseOptions{}
	cmd := &cobra.Command{
		Use:   "advise [<provider>/<model> <field>]",
		Short: "Review and apply catalog-based model configuration recommendations",
		Long: `Show model settings that differ from a verified catalog profile.

With a model and field, --accept follow-catalog removes the explicit leaf when
that is safe, while --accept pin writes the recommended value into config.yaml,
resolving an inherited value at the narrowest write location that keeps every
other recommendation intact. --keep-current records an exact value fingerprint
so the same recommendation stays quiet without suppressing later catalog or
configuration changes.

Without a model and field, the same flags apply to every active recommendation:
--accept pin resolves all of them, --accept follow-catalog does so only when
every one is a removable direct leaf, and --keep-current acknowledges all of
them.

All operations are offline. Writes preserve the YAML structure as far as the
selected edit permits and resolve a candidate configuration before replacing
the file.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 && len(args) != 2 {
				return fmt.Errorf("expected no arguments, or <provider>/<model> and <field>")
			}
			if len(args) == 2 && strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("model reference must not be empty")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigAdvise(cmd.Context(), cmd.OutOrStdout(), args, *opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write recommendations as JSON")
	cmd.Flags().StringVar(&opts.Accept, "accept", "", "Accept this recommendation, or all of them without a target: follow-catalog or pin")
	cmd.Flags().BoolVar(&opts.Keep, "keep-current", false, "Keep this exact recommendation's current value, or every active one without a target")
	return cmd
}

func runConfigAdvise(ctx context.Context, out io.Writer, args []string, opts configAdviseOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Accept != "" && opts.Accept != "follow-catalog" && opts.Accept != "pin" {
		return fmt.Errorf("--accept must be follow-catalog or pin")
	}
	if opts.Accept != "" && opts.Keep {
		return fmt.Errorf("--accept and --keep-current cannot be combined")
	}
	globalPath, err := config.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	projectPath := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		projectPath = config.ProjectConfigPath(cwd)
	}
	rc, err := config.LoadResolvedConfig(globalPath, projectPath)
	if err != nil {
		return err
	}
	all := config.CatalogConfigAdvisories(rc)
	advisories := all
	if len(args) == 2 {
		provider, model := config.SplitProviderModelRef(args[0])
		advisories = slices.DeleteFunc(slices.Clone(all), func(advisory config.CatalogConfigAdvisory) bool {
			return advisory.Provider != provider || advisory.Model != model || advisory.Field != args[1]
		})
		if len(advisories) == 0 {
			return fmt.Errorf("no active catalog recommendation for %s %s", args[0], args[1])
		}
	}
	if opts.Accept == "" && !opts.Keep {
		return renderConfigAdvisories(out, advisories, opts.JSON)
	}
	if len(advisories) == 0 {
		_, err := fmt.Fprintln(out, "No active catalog configuration recommendations.")
		return err
	}
	if opts.Keep {
		groups := [][]config.CatalogConfigAdvisory{config.CatalogConfigAdvisoryGroup(all, advisories[0])}
		if len(args) == 0 {
			groups = config.CatalogConfigAdvisoryGroups(all)
		}
		return recordCatalogAdvisoryKeeps(out, groups)
	}
	if len(args) == 0 {
		return runConfigAdviseApplyAll(ctx, out, globalPath, projectPath, advisories, opts.Accept)
	}
	return runConfigAdviseApplyOne(ctx, out, globalPath, projectPath, advisories[0], opts.Accept)
}

// runConfigAdviseApplyOne applies one recommendation and prints what changed.
func runConfigAdviseApplyOne(ctx context.Context, out io.Writer, globalPath, projectPath string, advisory config.CatalogConfigAdvisory, accept string) error {
	if accept == "follow-catalog" && !advisory.CanFollowCatalog {
		return fmt.Errorf("cannot follow the catalog for %s; %s", advisory.Path, catalogAdviceFollowHint(advisory))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	note, err := applyConfigCatalogAdvisory(ctx, globalPath, projectPath, advisory, accept)
	if err != nil {
		return err
	}
	if note == "" {
		fmt.Fprintf(out, "Applied %s for %s from catalog profile %s.\n", accept, advisory.Path, advisory.CatalogID)
		return nil
	}
	fmt.Fprintf(out, "Applied %s for %s from catalog profile %s (%s).\n", accept, advisory.Path, advisory.CatalogID, note)
	return nil
}

// runConfigAdviseApplyAll resolves every active recommendation with one
// command, re-resolving the remaining recommendations after each edit. The
// first recommendation that cannot be resolved stops the command; edits
// already applied stay valid, so re-running resumes with the rest.
func runConfigAdviseApplyAll(ctx context.Context, out io.Writer, globalPath, projectPath string, advisories []config.CatalogConfigAdvisory, accept string) error {
	reviewed := make(map[catalogAdvisoryIdentity]bool, len(advisories))
	for _, advisory := range advisories {
		reviewed[catalogAdvisoryIdentityOf(advisory)] = true
	}
	if accept == "follow-catalog" {
		for _, group := range config.CatalogConfigAdvisoryGroups(advisories) {
			advisory := group[0]
			if !advisory.CanFollowCatalog {
				return fmt.Errorf("cannot follow the catalog for %s; %s", advisory.Path, catalogAdviceFollowHint(advisory))
			}
		}
	}
	active := advisories
	for len(active) > 0 {
		advisory := config.CatalogConfigAdvisoryGroup(active, active[0])[0]
		if err := runConfigAdviseApplyOne(ctx, out, globalPath, projectPath, advisory, accept); err != nil {
			return err
		}
		rc, err := config.LoadResolvedConfig(globalPath, projectPath)
		if err != nil {
			return fmt.Errorf("reload config after edit: %w", err)
		}
		next := config.CatalogConfigAdvisories(rc)
		resolved := true
		for _, candidate := range next {
			identity := catalogAdvisoryIdentityOf(candidate)
			if !reviewed[identity] {
				return fmt.Errorf("the edit introduced recommendation %s; run chord config advise again", candidate.Path)
			}
			if identity == catalogAdvisoryIdentityOf(advisory) {
				resolved = false
			}
		}
		if !resolved {
			return fmt.Errorf("recommendation %s is still active after the edit; run chord config advise again", advisory.Path)
		}
		active = next
	}
	return nil
}

// recordCatalogAdvisoryKeeps acknowledges one or more shared declarations.
func recordCatalogAdvisoryKeeps(out io.Writer, groups [][]config.CatalogConfigAdvisory) error {
	advisories := make([]config.CatalogConfigAdvisory, 0, len(groups))
	for _, group := range groups {
		advisories = append(advisories, group...)
	}
	if err := config.RecordCatalogConfigAdvisoryAcknowledgments(advisories...); err != nil {
		return err
	}
	for _, group := range groups {
		advisory := group[0]
		if len(group) > 1 {
			fmt.Fprintf(out, "Kept current value for %d bindings that inherit %s under catalog profile %s.\n", len(group), advisory.OriginRef(), advisory.CatalogID)
			continue
		}
		fmt.Fprintf(out, "Kept current value for %s under catalog profile %s.\n", advisory.Path, advisory.CatalogID)
	}
	return nil
}

func renderConfigAdvisories(out io.Writer, advisories []config.CatalogConfigAdvisory, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(advisories)
	}
	if len(advisories) == 0 {
		_, err := fmt.Fprintln(out, "No active catalog configuration recommendations.")
		return err
	}
	for _, group := range config.CatalogConfigAdvisoryGroups(advisories) {
		advisory := group[0]
		accept := "pin"
		if advisory.CanFollowCatalog {
			accept = "follow-catalog"
		}
		fmt.Fprintf(out, "%s: current=%s, recommended=%s, catalog=%s\n",
			advisory.Path,
			formatCatalogCLIValue(advisory.Current),
			formatCatalogCLIValue(advisory.Recommended),
			advisory.CatalogID)
		if len(group) > 1 {
			fmt.Fprintf(out, "  shared: %s (%s)\n", advisory.OriginRef(), config.CatalogConfigAdvisoryBindingList(group))
		}
		fmt.Fprintf(out, "  accept: chord config advise %s %s --accept %s\n", advisory.Provider+"/"+advisory.Model, advisory.Field, accept)
		if !advisory.CanPin && !advisory.CanFollowCatalog {
			fmt.Fprintf(out, "  fix:    %s\n", catalogAdviceManualFixHint(advisory))
		}
		fmt.Fprintf(out, "  keep:   chord config advise %s %s --keep-current", advisory.Provider+"/"+advisory.Model, advisory.Field)
		if len(group) > 1 {
			fmt.Fprintf(out, "  (keeps all %d)", len(group))
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "Apply every recommendation: chord config advise --accept pin")
	fmt.Fprintln(out, "Keep every current value:   chord config advise --keep-current")
	return nil
}

// catalogAdviceFollowHint explains how to change a value that --accept
// follow-catalog cannot remove, because it is not a removable direct leaf.
func catalogAdviceFollowHint(advisory config.CatalogConfigAdvisory) string {
	if advisory.CanPin {
		if ref := advisory.OriginRef(); ref != "" {
			return "use --accept pin or edit the value at " + ref
		}
		return "use --accept pin or edit the value directly"
	}
	return "use --accept pin to add an explicit override, or " + catalogAdviceManualFixHint(advisory)
}

// catalogAdviceManualFixHint describes the hand edit that changes an
// inherited value. Pin expansion reports it when no safe write location is
// found, and the listing keeps it as the fallback next to the pin command.
func catalogAdviceManualFixHint(advisory config.CatalogConfigAdvisory) string {
	hint := "set " + formatCatalogCLIValue(advisory.Recommended) + " by expanding the inherited entry to add an explicit override under " + advisory.Path
	if ref := advisory.OriginRef(); ref != "" {
		hint += ", or by editing the declaring source " + ref
	}
	return hint
}

func formatCatalogCLIValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func applyConfigCatalogAdvisory(ctx context.Context, globalPath, projectPath string, advisory config.CatalogConfigAdvisory, accept string) (string, error) {
	targetPath := globalPath
	if advisory.CurrentOrigin.Layer == config.OriginLayerProject {
		targetPath = projectPath
	}
	if targetPath == "" || advisory.CurrentOrigin.Layer == config.OriginLayerCatalog {
		return "", fmt.Errorf("recommendation %s does not have a writable user config origin", advisory.Path)
	}
	note := ""
	err := config.CreateOrUpdateConfigFileLocked(targetPath, func(current []byte) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fresh, err := config.LoadResolvedConfig(globalPath, projectPath)
		if err != nil {
			return nil, fmt.Errorf("reload config before edit: %w", err)
		}
		active := config.CatalogConfigAdvisories(fresh)
		var currentAdvisory *config.CatalogConfigAdvisory
		for _, candidate := range active {
			if candidate.Provider == advisory.Provider &&
				candidate.Model == advisory.Model &&
				candidate.Field == advisory.Field {
				copy := candidate
				currentAdvisory = &copy
				break
			}
		}
		if currentAdvisory == nil ||
			currentAdvisory.CurrentFingerprint != advisory.CurrentFingerprint ||
			currentAdvisory.RecommendedFingerprint != advisory.RecommendedFingerprint ||
			currentAdvisory.CatalogID != advisory.CatalogID ||
			currentAdvisory.CatalogVersion != advisory.CatalogVersion ||
			currentAdvisory.CurrentOrigin.Layer != advisory.CurrentOrigin.Layer ||
			currentAdvisory.CurrentOrigin.File != advisory.CurrentOrigin.File ||
			currentAdvisory.CanPin != advisory.CanPin ||
			currentAdvisory.CanFollowCatalog != advisory.CanFollowCatalog {
			return nil, fmt.Errorf("recommendation %s changed while it was being reviewed; run chord config advise again", advisory.Path)
		}
		if advisory.CanPin {
			if err := guardCatalogAdvisoryDirectDeclaration(current, advisory, active); err != nil {
				return nil, err
			}
		}
		if !advisory.CanPin && accept == "pin" {
			edited, detail, err := applyCatalogAdvisoryPinExpansion(current, globalPath, projectPath, targetPath, advisory, active)
			if err != nil {
				return nil, err
			}
			note = detail
			return edited, nil
		}
		edited, err := editConfigYAMLForCatalogAdvisory(current, advisory, accept)
		if err != nil {
			return nil, err
		}
		candidate, err := loadConfigCatalogAdvisoryCandidate(globalPath, projectPath, targetPath, edited)
		if err != nil {
			return nil, err
		}
		if hasConfigErrors(candidate.Diagnostics) {
			return nil, fmt.Errorf("candidate config resolution failed")
		}
		return edited, nil
	})
	if err != nil {
		return "", err
	}
	return note, nil
}

func loadConfigCatalogAdvisoryCandidate(globalPath, projectPath, targetPath string, edited []byte) (*config.ResolvedConfig, error) {
	tmp, err := os.CreateTemp("", "chord-config-advise-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("create candidate temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(edited); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write candidate temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("write candidate temp file: %w", err)
	}
	if targetPath == globalPath {
		return config.LoadResolvedConfig(tmpPath, projectPath)
	}
	if targetPath == projectPath {
		return config.LoadResolvedConfig(globalPath, tmpPath)
	}
	return nil, fmt.Errorf("candidate target is not a config layer")
}
