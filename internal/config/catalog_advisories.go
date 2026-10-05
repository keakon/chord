package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
)

// CatalogFreshnessAdvisories reports verified catalog references that a newer
// same-family entry likely supersedes. The heuristic is advisory-only: the
// gateway behind a wire name may genuinely still serve the older model, so
// the output never fails loading and callers must never rebind automatically.
// Acknowledged references (see RecordCatalogAdvisoryAcknowledgment) stay
// silent for the catalog version current when they were acknowledged.
func CatalogFreshnessAdvisories(cfg *Config) []string {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil
	}
	acks, err := loadCatalogAdvisoryAcks()
	if err != nil {
		acks = nil
	}
	version := modelcatalog.Version()
	var out []string
	for _, providerName := range slices.Sorted(maps.Keys(cfg.Providers)) {
		provider := cfg.Providers[providerName]
		for _, modelName := range slices.Sorted(maps.Keys(provider.Models)) {
			model := provider.Models[modelName]
			if model.Catalog == nil || model.Catalog.Disabled || strings.TrimSpace(model.Catalog.ID) == "" {
				continue
			}
			if acknowledged(providerName, modelName, version, acks) {
				continue
			}
			newer, ok := modelcatalog.SuggestNewerVersion(modelName, model.Catalog.ID)
			if !ok {
				continue
			}
			out = append(out, fmt.Sprintf(
				"providers.%s.models.%s borrows catalog model %q; newer verified entry %q exists — re-run %q to rebind, or %q to keep the current borrow",
				providerName, modelName, model.Catalog.ID, newer.ModelID,
				"chord config add "+providerName+"/"+modelName+" --catalog "+newer.ModelID,
				"chord config add "+providerName+"/"+modelName+" --keep-current",
			))
		}
	}
	return out
}

type catalogAdvisoryAck struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	CatalogVersion string `json:"catalog_version"`
}

type catalogAdvisoryAckFile struct {
	Acks []catalogAdvisoryAck `json:"acks"`
}

// RecordCatalogAdvisoryAcknowledgment silences freshness advisories for one
// model reference under the current catalog version. A later catalog change
// re-arms the advisory, because the facts the user chose to keep are new ones.
func RecordCatalogAdvisoryAcknowledgment(provider, model string) error {
	path, err := catalogAdvisoryAckPath()
	if err != nil {
		return err
	}
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	var state catalogAdvisoryAckFile
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	entry := catalogAdvisoryAck{Provider: provider, Model: model, CatalogVersion: modelcatalog.Version()}
	if slices.Contains(state.Acks, entry) {
		return nil
	}
	state.Acks = append(state.Acks, entry)
	data, err := json.MarshalIndent(&state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create advisory state dir: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func catalogAdvisoryAckPath() (string, error) {
	home, err := ConfigHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config home: %w", err)
	}
	return filepath.Join(home, "catalog-advisories.json"), nil
}

func loadCatalogAdvisoryAcks() ([]catalogAdvisoryAck, error) {
	path, err := catalogAdvisoryAckPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state catalogAdvisoryAckFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return state.Acks, nil
}

func acknowledged(provider, model, catalogVersion string, acks []catalogAdvisoryAck) bool {
	for _, ack := range acks {
		if ack.Provider == provider && ack.Model == model && ack.CatalogVersion == catalogVersion {
			return true
		}
	}
	return false
}
