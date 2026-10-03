package modelcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheSchemaVersion guards the refresh cache file. A binary that does not
// know a newer cache shape rejects the file instead of misreading it and
// falls back to the embedded snapshot; catalog field additions are guarded
// the same way by the strict catalog decode inside the cache.
const CacheSchemaVersion = 1

// Candidate is one discovery entry delivered through the refresh cache from
// the upstream repository's candidates set: a wire model name observed on a
// provider scope, together with whatever facts could be verified so far. A
// candidate never fills runtime defaults — it shows up in discovery lists
// with its scope and sources, and adopting it is always an explicit user
// choice that writes the values into the user's config as its own.
type Candidate struct {
	WireModelID string `json:"wire_model_id"`
	// Scope names the provider side the wire name was observed on: a managed
	// preset ID or a gateway identifier. Suggestions prefer the scope that
	// matches the endpoint being configured and never pick one silently.
	Scope string `json:"scope"`
	// ModelID references a verified catalog model when the sighting is known
	// to resemble one; borrowing that model is the verified adoption path.
	ModelID string `json:"model_id,omitempty"`
	// Context, Input, Output and InputModalities are observed reference
	// values; zero/absent means not observed, never a default.
	Context         int      `json:"context,omitempty"`
	Input           int      `json:"input,omitempty"`
	Output          int      `json:"output,omitempty"`
	InputModalities []string `json:"input_modalities,omitempty"`
	Sources         []Source `json:"sources"`
	Notes           string   `json:"notes,omitempty"`
}

// ValidateAgainst checks the candidate against the catalog it ships with. It
// runs both at refresh time (source side) and at cache load time (runtime
// side), so a candidate that references an unknown model or carries an
// unverifiable fact never reaches a suggestion list.
func (c Candidate) ValidateAgainst(catalog *Catalog) error {
	if strings.TrimSpace(c.WireModelID) == "" {
		return errors.New("wire_model_id is required")
	}
	if strings.Contains(c.WireModelID, "@") {
		return fmt.Errorf("wire model name %q must not contain @ (collides with the variant reference syntax)", c.WireModelID)
	}
	if strings.TrimSpace(c.Scope) == "" {
		return errors.New("scope is required")
	}
	if c.ModelID != "" {
		if _, ok := catalog.byModelID[c.ModelID]; !ok {
			return fmt.Errorf("model %q is not in the catalog", c.ModelID)
		}
	}
	if c.Context < 0 || c.Input < 0 || c.Output < 0 {
		return errors.New("observed facts must not be negative; omit what was not observed")
	}
	if c.Input > 0 && c.Context > 0 && c.Input > c.Context {
		return fmt.Errorf("input %d exceeds context %d", c.Input, c.Context)
	}
	if c.Context == 0 && c.Output == 0 && c.ModelID == "" {
		return errors.New("a candidate needs observed facts or a catalog model reference")
	}
	for _, mod := range c.InputModalities {
		if !validModalities[mod] {
			return fmt.Errorf("unknown modality %q", mod)
		}
	}
	if len(c.Sources) == 0 {
		return errors.New("at least one source is required")
	}
	for _, s := range c.Sources {
		if !strings.HasPrefix(s.URL, "https://") {
			return fmt.Errorf("source URL must be https, got %q", s.URL)
		}
		if _, err := time.Parse(time.DateOnly, s.Checked); err != nil {
			return fmt.Errorf("checked date %q must be a calendar day in YYYY-MM-DD form", s.Checked)
		}
	}
	return nil
}

// CacheFile is the refresh cache written by `chord config refresh-catalog`:
// one complete catalog snapshot plus the candidates that shipped with it. The
// schema is chord-owned and decoded strictly, so a cache written by a newer
// chord, or catalog data with fields this binary does not know, fails the
// load and the process falls back to the embedded snapshot. The cache never
// carries credentials or request payloads.
type CacheFile struct {
	SchemaVersion int         `json:"schema_version"`
	Repository    string      `json:"repository"`
	Revision      string      `json:"revision"` // upstream tag the snapshot was pinned to
	Commit        string      `json:"commit,omitempty"`
	FetchedAt     string      `json:"fetched_at"` // RFC 3339 UTC
	Catalog       *Catalog    `json:"catalog"`
	Candidates    []Candidate `json:"candidates,omitempty"`
}

// ReadCacheFile loads and validates a refresh cache. The decode is strict on
// every level, so unknown fields anywhere in the file reject the cache.
func ReadCacheFile(path string) (*CacheFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f CacheFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse cache %s: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("cache %s: %w", path, err)
	}
	return &f, nil
}

func (f *CacheFile) validate() error {
	if f.SchemaVersion != CacheSchemaVersion {
		return fmt.Errorf("cache schema version %d is not supported (want %d); refresh the catalog again with this chord version", f.SchemaVersion, CacheSchemaVersion)
	}
	if strings.TrimSpace(f.Repository) == "" {
		return errors.New("cache repository is required")
	}
	if strings.TrimSpace(f.Revision) == "" {
		return errors.New("cache revision is required")
	}
	if _, err := time.Parse(time.RFC3339, f.FetchedAt); err != nil {
		return fmt.Errorf("fetched_at %q must be an RFC 3339 timestamp", f.FetchedAt)
	}
	if f.Catalog == nil {
		return errors.New("cache has no catalog")
	}
	if err := f.Catalog.validate(); err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	f.Catalog.buildIndexes()
	// The wrapper pins the upstream revision the snapshot was fetched from;
	// the catalog carries it so every origin view reports the same record.
	f.Catalog.Source = &CatalogSource{Repository: f.Repository, Revision: f.Revision}
	seen := make(map[[2]string]bool, len(f.Candidates))
	for i := range f.Candidates {
		c := f.Candidates[i]
		if err := c.ValidateAgainst(f.Catalog); err != nil {
			return fmt.Errorf("candidate %d: %w", i, err)
		}
		key := [2]string{c.WireModelID, c.Scope}
		if seen[key] {
			return fmt.Errorf("candidate %q on scope %q appears twice", c.WireModelID, c.Scope)
		}
		seen[key] = true
	}
	return nil
}

// WriteCacheFile serializes the cache and replaces path atomically: write to
// a temp file in the same directory, fsync, rename. A reader sees either the
// previous complete cache or the new one, never a torn file. Concurrent
// writers must serialize the call themselves and re-check versions under
// their lock; the write itself never merges with the previous content — a
// cache is always one complete catalog snapshot.
func WriteCacheFile(path string, f *CacheFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog cache: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create cache temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if n, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write cache temp file: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write cache temp file: %w", errShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync cache temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close cache temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace cache file: %w", err)
	}
	return nil
}

var errShortWrite = errors.New("short write")
