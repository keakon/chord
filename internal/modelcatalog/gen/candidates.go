package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
)

const candidatesDirName = "candidates"

// candidateSource is one discovery entry under candidates/. Every fact is
// optional except the wire name, the scope, and at least one source: a
// candidate carries whatever could be observed, never a completed verified
// record.
type candidateSource struct {
	WireModelID     string      `yaml:"wire_model_id"`
	Released        string      `yaml:"released"`
	CodingSources   []sourceRef `yaml:"coding_sources"`
	Scope           string      `yaml:"scope"`
	ModelID         string      `yaml:"model_id"`
	Context         int         `yaml:"context"`
	Input           int         `yaml:"input"`
	Output          int         `yaml:"output"`
	InputModalities []string    `yaml:"input_modalities"`
	Sources         []sourceRef `yaml:"sources"`
	Notes           string      `yaml:"notes"`
}

// LoadCandidates reads every candidates/*.yaml entry in dir and validates it
// against the verified catalog. A missing candidates directory is the normal
// upstream state, not an error. The result is sorted by wire model ID and
// scope so a refresh cache is byte-stable regardless of file order.
func LoadCandidates(dir string, catalog *modelcatalog.Catalog) ([]modelcatalog.Candidate, error) {
	entries, err := os.ReadDir(filepath.Join(dir, candidatesDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read candidates dir: %w", err)
	}
	var out []modelcatalog.Candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, candidatesDirName, entry.Name())
		var src candidateSource
		if err := decodeYAMLFile(path, &src); err != nil {
			return nil, err
		}
		converted, err := convertCandidate(src, path, catalog)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	slices.SortFunc(out, func(a, b modelcatalog.Candidate) int {
		if n := strings.Compare(a.WireModelID, b.WireModelID); n != 0 {
			return n
		}
		return strings.Compare(a.Scope, b.Scope)
	})
	for i := 1; i < len(out); i++ {
		if out[i].WireModelID == out[i-1].WireModelID && out[i].Scope == out[i-1].Scope {
			return nil, fmt.Errorf("candidate %q on scope %q appears twice", out[i].WireModelID, out[i].Scope)
		}
	}
	return out, nil
}

func convertCandidate(src candidateSource, path string, catalog *modelcatalog.Catalog) (modelcatalog.Candidate, error) {
	for _, s := range src.Sources {
		if err := s.validate(fmt.Sprintf("candidate %q source in %s", src.WireModelID, filepath.Base(path))); err != nil {
			return modelcatalog.Candidate{}, err
		}
	}
	c := modelcatalog.Candidate{
		WireModelID:     strings.TrimSpace(src.WireModelID),
		Released:        src.Released,
		Scope:           strings.TrimSpace(src.Scope),
		ModelID:         strings.TrimSpace(src.ModelID),
		Context:         src.Context,
		Input:           src.Input,
		Output:          src.Output,
		InputModalities: slices.Clone(src.InputModalities),
		Notes:           strings.TrimSpace(src.Notes),
	}
	for _, s := range src.CodingSources {
		c.CodingSources = append(c.CodingSources, modelcatalog.Source{URL: s.URL, Checked: s.Checked})
	}
	for _, s := range src.Sources {
		c.Sources = append(c.Sources, modelcatalog.Source{URL: s.URL, Checked: s.Checked})
	}
	if err := c.ValidateAgainst(catalog); err != nil {
		return modelcatalog.Candidate{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return c, nil
}
