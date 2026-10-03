package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

// loadFixtureCatalog loads the committed sources as the reference catalog for
// candidate validation.
func loadFixtureCatalog(t *testing.T) *modelcatalog.Catalog {
	t.Helper()
	c, err := Load(filepath.Join("..", "data"))
	if err != nil {
		t.Fatalf("load committed sources: %v", err)
	}
	return c
}

func writeCandidateDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if len(files) == 0 {
		return dir
	}
	if err := os.MkdirAll(filepath.Join(dir, "candidates"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, "candidates", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const validCandidate = `wire_model_id: gpt-6.1-sol-messages
scope: gateway-a
model_id: openai/gpt-6.1-sol
context: 400000
sources:
  - url: https://example.invalid/sighting
    checked: 2026-10-02
`

func TestLoadCandidates(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	if candidates, err := LoadCandidates(t.TempDir(), catalog); err != nil || len(candidates) != 0 {
		t.Fatalf("a missing candidates directory is the normal upstream state, got %v, %v", candidates, err)
	}

	dir := writeCandidateDir(t, map[string]string{
		"b.yaml": validCandidate,
		// Load order must not decide result order: the a-file sorts first.
		"a.yaml": `wire_model_id: gpt-6.1-sol-messages
scope: gateway-b
context: 400000
output: 128000
input_modalities: [text, image]
sources:
  - url: https://example.invalid/other
    checked: 2026-10-02
notes: read off the model list
`,
		"notes.txt": "not a candidate file, ignored",
	})
	candidates, err := LoadCandidates(dir, catalog)
	if err != nil {
		t.Fatalf("LoadCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(candidates))
	}
	if candidates[0].Scope != "gateway-a" || candidates[1].Scope != "gateway-b" {
		t.Fatalf("candidates must sort by wire name then scope: %+v", candidates)
	}
	if candidates[1].Notes == "" || len(candidates[1].InputModalities) != 2 {
		t.Fatalf("optional fields must survive conversion: %+v", candidates[1])
	}
}

func TestLoadCandidatesRejects(t *testing.T) {
	catalog := loadFixtureCatalog(t)
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			"unknown field",
			validCandidate + "future_field: true\n",
			"field future_field not found",
		},
		{
			"unknown model reference",
			"wire_model_id: m\nscope: gw\nmodel_id: openai/absent\nsources:\n  - url: https://example.invalid/m\n    checked: 2026-10-02\n",
			"not in the catalog",
		},
		{
			"missing sources",
			"wire_model_id: m\nscope: gw\nmodel_id: openai/gpt-6.1-sol\n",
			"source is required",
		},
		{
			"at sign in wire name",
			"wire_model_id: m@x\nscope: gw\nmodel_id: openai/gpt-6.1-sol\nsources:\n  - url: https://example.invalid/m\n    checked: 2026-10-02\n",
			"@",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeCandidateDir(t, map[string]string{"c.yaml": tc.content})
			_, err := LoadCandidates(dir, catalog)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadCandidates() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadCandidatesRejectsDuplicates(t *testing.T) {
	catalog := loadFixtureCatalog(t)
	dir := writeCandidateDir(t, map[string]string{
		"one.yaml": validCandidate,
		"two.yaml": validCandidate,
	})
	if _, err := LoadCandidates(dir, catalog); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate wire/scope sightings must fail, got %v", err)
	}
}
